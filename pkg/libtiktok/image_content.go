package libtiktok

import (
	"bytes"
	"strings"

	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
)

func parsePrivateMediaFromConversationEntryProto(entry *tiktokpb.ConversationMessageEntry) (msgType, mediaURL, thumbnailURL, decryptKey string, width, height, durationMs int, ok bool) {
	if entry == nil {
		return "", "", "", "", 0, 0, 0, false
	}
	return parsePrivateMediaAttachmentProto(entry.GetMessageSubtype(), entry.GetPrivateImage())
}

func parsePrivateMediaFromWebsocketDetailProto(detail *tiktokpb.WebsocketMessageDetail) (msgType, mediaURL, thumbnailURL, decryptKey string, width, height, durationMs int, ok bool) {
	if detail == nil {
		return "", "", "", "", 0, 0, 0, false
	}
	return parsePrivateMediaAttachmentProto(detail.GetMessageSubtype(), detail.GetPrivateImage())
}

func parsePrivateMediaAttachmentProto(messageSubtype string, attachment *tiktokpb.PrivateImageAttachment) (msgType, mediaURL, thumbnailURL, decryptKey string, width, height, durationMs int, ok bool) {
	if attachment == nil {
		return "", "", "", "", 0, 0, 0, false
	}

	switch {
	case strings.EqualFold(messageSubtype, "private_video"):
		return parsePrivateVideoAttachmentProto(attachment)
	default:
		return parsePrivateImageAttachmentProto(messageSubtype, attachment)
	}
}

func parsePrivateImageAttachmentProto(messageSubtype string, attachment *tiktokpb.PrivateImageAttachment) (msgType, mediaURL, thumbnailURL, decryptKey string, width, height, durationMs int, ok bool) {
	decryptKey = attachment.GetDecryptKey()
	fullURL := ""
	fullWidth := 0
	fullHeight := 0
	thumbURL := ""
	thumbWidth := 0
	thumbHeight := 0
	firstURL := ""
	firstWidth := 0
	firstHeight := 0

	for _, variant := range attachment.GetVariants() {
		url := firstNonEmptyString(variant.GetUrl())
		if url == "" {
			continue
		}

		vw := int(variant.GetWidth())
		vh := int(variant.GetHeight())
		if firstURL == "" {
			firstURL = url
			firstWidth = vw
			firstHeight = vh
		}

		label := normalizePrivateMediaVariantLabel(variant.GetLabel())
		switch {
		case label == "full" || strings.Contains(strings.ToLower(url), "default.image"):
			if fullURL == "" {
				fullURL = url
				fullWidth = vw
				fullHeight = vh
			}
		case label == "view" || strings.Contains(strings.ToLower(url), "thumb.image"):
			if thumbURL == "" {
				thumbURL = url
				thumbWidth = vw
				thumbHeight = vh
			}
		}
	}

	if fullURL == "" {
		fullURL = firstURL
		fullWidth = firstWidth
		fullHeight = firstHeight
	}
	if thumbURL == "" {
		thumbURL = fullURL
		thumbWidth = fullWidth
		thumbHeight = fullHeight
	}

	ok = strings.EqualFold(messageSubtype, "private_image") ||
		decryptKey != "" ||
		fullURL != "" ||
		thumbURL != ""
	if !ok {
		return "", "", "", "", 0, 0, 0, false
	}

	if fullWidth != 0 {
		width = fullWidth
		height = fullHeight
	} else {
		width = thumbWidth
		height = thumbHeight
	}
	return "image", fullURL, thumbURL, decryptKey, width, height, 0, true
}

func parsePrivateVideoAttachmentProto(attachment *tiktokpb.PrivateImageAttachment) (msgType, mediaURL, thumbnailURL, decryptKey string, width, height, durationMs int, ok bool) {
	playURL := ""
	playWidth := 0
	playHeight := 0
	playDuration := 0
	coverURL := ""
	coverWidth := 0
	coverHeight := 0
	firstURL := ""
	firstWidth := 0
	firstHeight := 0

	for _, variant := range attachment.GetVariants() {
		url := firstNonEmptyString(variant.GetUrl())
		if url == "" {
			continue
		}

		vw := int(variant.GetWidth())
		vh := int(variant.GetHeight())
		if firstURL == "" {
			firstURL = url
			firstWidth = vw
			firstHeight = vh
		}

		label := normalizePrivateMediaVariantLabel(variant.GetLabel())
		switch {
		case label == "play" || strings.Contains(strings.ToLower(url), "mime_type=video_"):
			if playURL == "" {
				playURL = url
				playWidth = vw
				playHeight = vh
				playDuration = int(variant.GetDurationMs())
			}
		case label == "cover" || strings.Contains(strings.ToLower(url), "tplv-noop.image"):
			if coverURL == "" {
				coverURL = url
				coverWidth = vw
				coverHeight = vh
			}
		}
	}

	if playURL == "" {
		playURL = firstURL
		playWidth = firstWidth
		playHeight = firstHeight
	}
	if coverURL == "" {
		coverURL = playURL
		coverWidth = playWidth
		coverHeight = playHeight
	}

	if playURL == "" && coverURL == "" {
		return "", "", "", "", 0, 0, 0, false
	}

	if playWidth != 0 {
		width = playWidth
		height = playHeight
	} else {
		width = coverWidth
		height = coverHeight
	}
	return "video", playURL, coverURL, "", width, height, playDuration, true
}

func normalizePrivateMediaVariantLabel(label []byte) string {
	if len(label) == 0 {
		return ""
	}

	trimmed := strings.ToLower(strings.TrimSpace(string(label)))
	if trimmed != "" && isPrintableASCII(trimmed) {
		return trimmed
	}

	lowerBytes := bytes.ToLower(label)
	switch {
	case bytes.Contains(lowerBytes, []byte("full")):
		return "full"
	case bytes.Contains(lowerBytes, []byte("view")):
		return "view"
	case bytes.Contains(lowerBytes, []byte("play")):
		return "play"
	case bytes.Contains(lowerBytes, []byte("cover")):
		return "cover"
	default:
		return ""
	}
}

func firstNonEmptyString(values []string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func isPrintableASCII(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}
