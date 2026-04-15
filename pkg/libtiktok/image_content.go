package libtiktok

import (
	"bytes"
	"strings"

	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
)

func parsePrivateImageFromConversationEntryProto(entry *tiktokpb.ConversationMessageEntry) (mediaURL, thumbnailURL, decryptKey string, width, height int, ok bool) {
	if entry == nil {
		return "", "", "", 0, 0, false
	}
	return parsePrivateImageAttachmentProto(entry.GetMessageSubtype(), entry.GetPrivateImage())
}

func parsePrivateImageFromWebsocketDetailProto(detail *tiktokpb.WebsocketMessageDetail) (mediaURL, thumbnailURL, decryptKey string, width, height int, ok bool) {
	if detail == nil {
		return "", "", "", 0, 0, false
	}
	return parsePrivateImageAttachmentProto(detail.GetMessageSubtype(), detail.GetPrivateImage())
}

func parsePrivateImageAttachmentProto(messageSubtype string, attachment *tiktokpb.PrivateImageAttachment) (mediaURL, thumbnailURL, decryptKey string, width, height int, ok bool) {
	if attachment == nil {
		return "", "", "", 0, 0, false
	}

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

		label := normalizePrivateImageVariantLabel(variant.GetLabel())
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
		return "", "", "", 0, 0, false
	}

	if fullWidth != 0 {
		width = fullWidth
		height = fullHeight
	} else {
		width = thumbWidth
		height = thumbHeight
	}
	return fullURL, thumbURL, decryptKey, width, height, true
}

func normalizePrivateImageVariantLabel(label []byte) string {
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
