package libtiktok

import (
	"context"
	"encoding/json"
	"testing"
)

// A shared photo post (photomode). The shape is from a real DM; the IDs and
// host are placeholders, since a genuine uid names the account that sent it.
// Before aweType 810 was handled this produced the literal body
// "[unsupported message type: type_810]" and threw the cover away.
const photoPostContent = `{
  "aweType": 810,
  "content_name": "Never broke again",
  "cover_url": {
    "uri": "tos-alisg-i-photomode-sg/78e1bc022b98493baae9e6b532c85bf6",
    "url_list": [
      "https://p16-common-sign.tiktokcdn.com/cover.jpeg?x-expires=1786449600",
      "https://p19-common-sign.tiktokcdn.com/cover.jpeg?x-expires=1786449600"
    ]
  },
  "cover_height": 1920,
  "cover_width": 1080,
  "itemId": "7000000000000000001",
  "uid": "6000000000000000002"
}`

func TestParseMessageContent_photoPostAweType810(t *testing.T) {
	// A nil client means the uid cannot be resolved to a handle, so the
	// canonical link is skipped — the cover and caption must still come out.
	msgType, text, mediaURL, mimeType := parseMessageContent(
		context.Background(), nil, []byte(photoPostContent),
	)
	if msgType != "photo" {
		t.Fatalf("msgType = %q, want photo", msgType)
	}
	if mediaURL != "https://p16-common-sign.tiktokcdn.com/cover.jpeg?x-expires=1786449600" {
		t.Fatalf("mediaURL = %q, want the first cover URL", mediaURL)
	}
	if mimeType == "" {
		t.Fatal("mimeType is empty; the cover would upload with no type")
	}
	if text != "Never broke again" {
		t.Fatalf("text = %q, want the sound name as the caption", text)
	}
}

// A Tenor GIF sent from TikTok's sticker picker. Shape from a real DM, IDs
// replaced.
const tenorGifContent = `{
  "aweType": 502,
  "display_name": "GIF",
  "height": 112,
  "image_type": "gif",
  "sticker_id": "t:s:7000000000000000003",
  "url": {"uri": "", "url_list": ["https://media.tenor.com/example/gif.webp"]},
  "width": 112
}`

func TestParseMessageContent_tenorGifAweType502(t *testing.T) {
	msgType, text, mediaURL, mimeType := parseMessageContent(
		context.Background(), nil, []byte(tenorGifContent),
	)
	if msgType != "sticker" {
		t.Fatalf("msgType = %q, want sticker", msgType)
	}
	if mediaURL != "https://media.tenor.com/example/gif.webp" {
		t.Fatalf("mediaURL = %q", mediaURL)
	}
	if mimeType == "" {
		t.Fatal("mimeType is empty")
	}
	if text != "[GIF]" {
		t.Fatalf("text = %q, want [GIF]", text)
	}
}

// An aweType 502 with no usable URL must not claim to be a sticker with nothing
// to show — it should fall through to the unsupported notice instead.
func TestParseMessageContent_gifWithoutURLIsNotASticker(t *testing.T) {
	msgType, _, mediaURL, _ := parseMessageContent(
		context.Background(), nil, []byte(`{"aweType":502,"display_name":"GIF"}`),
	)
	if msgType == "sticker" || mediaURL != "" {
		t.Fatalf("msgType = %q, mediaURL = %q; want no sticker without a URL", msgType, mediaURL)
	}
}

func TestStringListFromContent(t *testing.T) {
	var content map[string]any
	if err := json.Unmarshal([]byte(photoPostContent), &content); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := stringListFromContent(content, "cover_url")
	if len(got) != 2 {
		t.Fatalf("cover_url list = %d entries, want 2", len(got))
	}

	// Shapes that must not panic or invent a URL.
	for _, key := range []string{"missing", "content_name", "cover_height"} {
		if urls := stringListFromContent(content, key); urls != nil {
			t.Fatalf("stringListFromContent(%q) = %v, want nil", key, urls)
		}
	}
}

func TestVideoDetailStatus(t *testing.T) {
	// 10204 is what TikTok returns for a post that is deleted, private or
	// region-blocked: the page hydrates fine but carries no itemStruct.
	unavailable := map[string]any{
		"__DEFAULT_SCOPE__": map[string]any{
			"webapp.video-detail": map[string]any{"statusCode": float64(10204)},
		},
	}
	if code, ok := videoDetailStatus(unavailable); !ok || code != 10204 {
		t.Fatalf("videoDetailStatus = (%d, %v), want (10204, true)", code, ok)
	}

	healthy := map[string]any{
		"__DEFAULT_SCOPE__": map[string]any{
			"webapp.video-detail": map[string]any{
				"statusCode": float64(0),
				"itemInfo":   map[string]any{},
			},
		},
	}
	if code, ok := videoDetailStatus(healthy); !ok || code != 0 {
		t.Fatalf("videoDetailStatus(healthy) = (%d, %v), want (0, true)", code, ok)
	}

	// A shell page has no scope at all; that is "unknown", not "available".
	if _, ok := videoDetailStatus(map[string]any{}); ok {
		t.Fatal("videoDetailStatus on an empty scope reported a status")
	}
}
