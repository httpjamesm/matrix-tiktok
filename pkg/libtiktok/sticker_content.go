package libtiktok

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
)

// httpsURLPattern matches absolute https URLs in JSON-ish text (including
// values that are still escaped inside larger string blobs).
var httpsURLPattern = regexp.MustCompile(`https://[a-zA-Z0-9._~:/?#\[\]@!$&'()*+,;=%\-]+`)

// jsonStringFromAny returns a UTF-8 string from a JSON-decoded value (string,
// json.Number, or []byte from alternate decoders).
func jsonStringFromAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		// encoding/json uses float64 for numbers unless UseNumber is set.
		if x == float64(int64(x)) {
			return fmt.Sprintf("%.0f", x)
		}
		return fmt.Sprint(x)
	case json.Number:
		return x.String()
	case []byte:
		return string(x)
	default:
		return ""
	}
}

// parseContentJSONObject decodes TikTok content_json (field 8). The server
// sometimes sends a JSON object directly and sometimes a JSON string containing
// a second JSON object; both shapes appear in live WS / get_by_conversation
// traffic.
func parseContentJSONObject(contentBytes []byte) (map[string]any, error) {
	var raw any
	if err := json.Unmarshal(contentBytes, &raw); err != nil {
		return nil, err
	}
	return anyToJSONObject(raw)
}

func anyToJSONObject(raw any) (map[string]any, error) {
	switch x := raw.(type) {
	case map[string]any:
		return x, nil
	case string:
		var inner any
		if err := json.Unmarshal([]byte(x), &inner); err != nil {
			return nil, err
		}
		return anyToJSONObject(inner)
	default:
		return nil, fmt.Errorf("content_json: want object or string-wrapped object, got %T", raw)
	}
}

// stringAt descends into nested map[string]any objects using string keys and
// returns the leaf as a string, or "" if any step is missing or not a map/string.
func stringAt(m map[string]any, keys ...string) string {
	cur := any(m)
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur, ok = mm[k]
		if !ok {
			return ""
		}
	}
	return jsonStringFromAny(cur)
}

// isStickerImageURL reports whether u is a TikTok DM sticker CDN link. The
// host pattern varies by region; some payloads only match after scraping raw
// JSON (URLs hidden inside escaped string values).
func isStickerImageURL(u string) bool {
	if !strings.HasPrefix(u, "https://") {
		return false
	}
	lu := strings.ToLower(u)
	if strings.Contains(lu, "tiktok-dm-sticker") {
		return true
	}
	// ByteDance image CDN used for signed DM stickers.
	if strings.Contains(lu, "ibyteimg.com") {
		if strings.Contains(lu, "sticker") {
			return true
		}
		// tplv segment used on sticker-set-frame / full sticker assets in captures.
		if strings.Contains(lu, "dhq7zx4c1p") && strings.Contains(lu, "webp") {
			return true
		}
	}
	return false
}

func scrapeStickerURLsInString(s string) []string {
	var out []string
	for _, m := range httpsURLPattern.FindAllString(s, -1) {
		if isStickerImageURL(m) {
			out = append(out, m)
		}
	}
	return out
}

// findStickerURLScrapeInBytes scans the raw content_json bytes for sticker CDN
// URLs. This catches URLs that only appear inside escaped JSON strings (so they
// are not separate JSON string leaves after Unmarshal).
func findStickerURLScrapeInBytes(raw []byte) string {
	for _, u := range scrapeStickerURLsInString(string(raw)) {
		if u != "" {
			return u
		}
	}
	return ""
}

// deepWireField21Sticker walks nested objects and reports whether any map has
// protobuf-json key "21" equal to the string "sticker" (inner chat row marker).
func deepWireField21Sticker(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if jsonStringFromAny(x["21"]) == "sticker" {
			return true
		}
		for _, vv := range x {
			if deepWireField21Sticker(vv) {
				return true
			}
		}
	case []any:
		for _, el := range x {
			if deepWireField21Sticker(el) {
				return true
			}
		}
	case string:
		if len(x) > 0 && (x[0] == '{' || x[0] == '[') {
			var nested any
			if err := json.Unmarshal([]byte(x), &nested); err == nil {
				return deepWireField21Sticker(nested)
			}
		}
	}
	return false
}

// stickerURLFromStringValue inspects a JSON string leaf: embedded JSON objects,
// a lone URL, or any sticker CDN URL substring (for partially escaped blobs).
func stickerURLFromStringValue(s string) string {
	if len(s) > 0 && (s[0] == '{' || s[0] == '[') {
		var nested any
		if err := json.Unmarshal([]byte(s), &nested); err == nil {
			if u := findStickerHTTPSURL(nested); u != "" {
				return u
			}
		}
	}
	if strings.HasPrefix(s, "https://") && isStickerImageURL(s) {
		return s
	}
	for _, u := range scrapeStickerURLsInString(s) {
		if u != "" {
			return u
		}
	}
	return ""
}

// parseStickerFromContentJSON detects TikTok DM sticker bodies (wire field 21 =
// "sticker") and extracts the signed CDN URL and a short display line when
// present. The URL normally lives at 20 → 7 → 1 → 2 in the JSON mirror of the
// inner protobuf (see get_by_conversation / WebSocket content_json captures).
//
// Detection also accepts: (1) nested "21":"sticker" anywhere under the root,
// and (2) any https URL containing tiktok-dm-sticker in the tree (fallback when
// the type marker is missing or nested outside our first parse path).
//
// raw must be the original content_json bytes so URLs that only appear inside
// escaped JSON strings are still discoverable via substring scrape.
func parseStickerFromContentJSON(content map[string]any, raw []byte) (mediaURL, text string, ok bool) {
	url := stringAt(content, "20", "7", "1", "2")
	if url == "" || !strings.HasPrefix(url, "https://") {
		url = findStickerHTTPSURL(content)
	}
	if url == "" && len(raw) > 0 {
		url = findStickerURLScrapeInBytes(raw)
	}
	isSticker := jsonStringFromAny(content["21"]) == "sticker" ||
		deepWireField21Sticker(content) ||
		url != ""
	if !isSticker {
		return "", "", false
	}
	ok = true
	mediaURL = url
	// Preview strings observed under 20 → 7 → 2 (localized "sent a sticker" / "[sticker]").
	for _, path := range [][]string{
		{"20", "7", "2", "3", "1"},
		{"20", "7", "2", "2", "1"},
		{"20", "7", "2", "1", "1"},
	} {
		if s := stringAt(content, path...); s != "" {
			text = s
			break
		}
	}
	return mediaURL, text, ok
}

func parseStickerFromConversationEntryProto(entry *tiktokpb.ConversationMessageEntry) (mediaURL, text, mimeType string, ok bool) {
	if entry == nil {
		return "", "", "", false
	}
	return parseStickerFromAttachmentProto(entry.GetMessageSubtype(), entry.GetAttachment())
}

func parseStickerFromWebsocketDetailProto(detail *tiktokpb.WebsocketMessageDetail) (mediaURL, text, mimeType string, ok bool) {
	if detail == nil {
		return "", "", "", false
	}
	return parseStickerFromAttachmentProto(detail.GetMessageSubtype(), detail.GetAttachment())
}

func parseStickerFromAttachmentProto(messageSubtype string, attachment *tiktokpb.MessageAttachmentPayload) (mediaURL, text, mimeType string, ok bool) {
	sticker := attachment.GetSticker()
	if sticker == nil {
		return "", "", "", false
	}

	asset := sticker.GetAsset()
	if asset != nil {
		mediaURL = asset.GetUrl()
	}

	displayTexts := sticker.GetDisplayTexts()
	if displayTexts != nil {
		for _, candidate := range []*tiktokpb.LocalizedText{
			displayTexts.GetBracketedSticker(),
			displayTexts.GetSentASticker(),
			displayTexts.GetReserved_1(),
		} {
			if candidate == nil {
				continue
			}
			if candidate.GetText() != "" {
				text = candidate.GetText()
				break
			}
		}
	}

	if strings.EqualFold(messageSubtype, "sticker") || mediaURL != "" {
		ok = true
		if text == "" {
			text = "[sticker]"
		}
		mimeType = guessStickerMIMEFromURL(mediaURL)
	}
	return mediaURL, text, mimeType, ok
}

func findStickerHTTPSURL(v any) string {
	switch x := v.(type) {
	case map[string]any:
		for _, vv := range x {
			if u := findStickerHTTPSURL(vv); u != "" {
				return u
			}
		}
	case []any:
		for _, el := range x {
			if u := findStickerHTTPSURL(el); u != "" {
				return u
			}
		}
	case string:
		return stickerURLFromStringValue(x)
	}
	return ""
}

// guessStickerMIMEFromURL picks a likely image MIME type before the HTTP
// response is available (Content-Type on download is authoritative).
func guessStickerMIMEFromURL(u string) string {
	lower := strings.ToLower(u)
	switch {
	case strings.Contains(lower, "awebp"), strings.Contains(lower, ".webp"):
		return "image/webp"
	case strings.Contains(lower, ".png"):
		return "image/png"
	case strings.Contains(lower, ".gif"):
		return "image/gif"
	case strings.Contains(lower, ".jpg"), strings.Contains(lower, ".jpeg"):
		return "image/jpeg"
	default:
		return "image/webp"
	}
}
