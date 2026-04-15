package libtiktok

import (
	"context"
	"testing"

	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
)

func TestParseReplyQuotedTextFromWire(t *testing.T) {
	raw := `{"content":"{\"aweType\":0,\"text\":\"test\"}","refmsg_content":"{\"aweType\":0,\"text\":\"test\"}","refmsg_uid":"1234567890123456789"}`
	got := parseReplyQuotedTextFromWire([]byte(raw))
	if got != "test" {
		t.Fatalf("quoted text = %q, want test", got)
	}
}

func TestParseMessageContent_replyAweType703(t *testing.T) {
	ctx := context.Background()
	body := []byte(`{"aweType":703,"text":"this is a reply"}`)
	msgType, text, _, _ := parseMessageContent(ctx, nil, body)
	if msgType != "text" {
		t.Fatalf("msgType = %q, want text", msgType)
	}
	if text != "this is a reply" {
		t.Fatalf("text = %q", text)
	}
}

func TestParseMessageContent_placeholderHackJSONIsNotText(t *testing.T) {
	ctx := context.Background()
	body := []byte(`{"hack":"1"}`)
	msgType, text, mediaURL, _ := parseMessageContent(ctx, nil, body)
	if msgType != "" || text != "" || mediaURL != "" {
		t.Fatalf("parseMessageContent(%q) = type=%q text=%q url=%q, want all empty", body, msgType, text, mediaURL)
	}
}

func TestParseMessageEntry_stickerOuterFields(t *testing.T) {
	ctx := context.Background()
	url := "https://p16-tiktok-dm-sticker-sign-sg.ibyteimg.com/tos-alisg-i-dhq7zx4c1p-sg/2e9e491e3d7e4cfc8e1224c325883d9b~tplv-dhq7zx4c1p-full.awebp?rk3s=00edd399&x-expires=1778798048&x-signature=o4fDT044pLJ2azU2xy2AqiTzLng%3D"
	entry := &tiktokpb.ConversationMessageEntry{
		ConversationId:  protoString("0:1:1111111111111111111:2222222222222222222"),
		ServerMessageId: protoUint64(7628368595001378322),
		TimestampUs:     protoUint64(1776117973663318),
		SenderUserId:    protoUint64(7583401074457215263),
		ContentJson:     []byte(`{"hack":"1"}`),
		MessageSubtype:  protoString("sticker"),
		Attachment: &tiktokpb.MessageAttachmentPayload{
			Sticker: &tiktokpb.StickerMessagePayload{
				Asset: &tiktokpb.StickerAsset{
					Url: protoString(url),
				},
				DisplayTexts: &tiktokpb.StickerDisplayTexts{
					SentASticker: &tiktokpb.LocalizedText{Text: protoString("sent a sticker")},
					BracketedSticker: &tiktokpb.LocalizedText{
						Text: protoString("[sticker]"),
					},
				},
			},
		},
	}
	msg, _, err := parseMessageEntry(ctx, nil, entry)
	if err != nil {
		t.Fatalf("parseMessageEntry returned error: %v", err)
	}
	if msg.Type != "sticker" {
		t.Fatalf("msg.Type = %q, want sticker", msg.Type)
	}
	if msg.Text != "[sticker]" {
		t.Fatalf("msg.Text = %q, want [sticker]", msg.Text)
	}
	if msg.MediaURL != url {
		t.Fatalf("msg.MediaURL = %q, want %q", msg.MediaURL, url)
	}
	if msg.MimeType != "image/webp" {
		t.Fatalf("msg.MimeType = %q, want image/webp", msg.MimeType)
	}
}

func TestParseStickerFromWebsocketDetailProto(t *testing.T) {
	url := "https://p16-tiktok-dm-sticker-sign-sg.ibyteimg.com/tos-alisg-i-dhq7zx4c1p-sg/a4575e2cb6804d6b807b5b615d9cf006~tplv-dhq7zx4c1p-sticker-set-frame.awebp?rk3s=00edd399&x-expires=1776573068&x-signature=0Z7gYI3iG%2BhodKbHhmViZGDUVa4%3D"
	detail := &tiktokpb.WebsocketMessageDetail{
		ContentJson:    []byte(`{}`),
		MessageSubtype: protoString("sticker"),
		Attachment: &tiktokpb.MessageAttachmentPayload{
			Sticker: &tiktokpb.StickerMessagePayload{
				Asset: &tiktokpb.StickerAsset{
					Url: protoString(url),
				},
				DisplayTexts: &tiktokpb.StickerDisplayTexts{
					BracketedSticker: &tiktokpb.LocalizedText{Text: protoString("[autocollant]")},
				},
			},
		},
	}
	mediaURL, text, mimeType, ok := parseStickerFromWebsocketDetailProto(detail)
	if !ok {
		t.Fatal("parseStickerFromWebsocketDetailProto returned ok=false")
	}
	if mediaURL != url {
		t.Fatalf("mediaURL = %q, want %q", mediaURL, url)
	}
	if text != "[autocollant]" {
		t.Fatalf("text = %q, want [autocollant]", text)
	}
	if mimeType != "image/webp" {
		t.Fatalf("mimeType = %q, want image/webp", mimeType)
	}
}

func TestParseMessageEntry_privateImageOuterFields(t *testing.T) {
	ctx := context.Background()
	fullURL := "https://p0-tiktok-dm-private-sg.tiktok.com/tos-alisg-i-9aa6gs5p9y-sg/blob~tplv-9aa6gs5p9y-get:default.image?sig=full"
	thumbURL := "https://p0-tiktok-dm-private-sg.tiktok.com/tos-alisg-i-9aa6gs5p9y-sg/blob~tplv-9aa6gs5p9y-get:thumb.image?sig=thumb"
	decryptKey := "d1fa73054e2e8df4caebb81821d4ff39279419f0e2469df10acece30bcf6c69b"
	entry := &tiktokpb.ConversationMessageEntry{
		ConversationId:  protoString("0:1:1111111111111111111:2222222222222222222"),
		ServerMessageId: protoUint64(7629097176463443476),
		TimestampUs:     protoUint64(1776287609366353),
		SenderUserId:    protoUint64(7583401074457215263),
		ContentJson:     []byte(`{"hack":"1"}`),
		MessageSubtype:  protoString("private_image"),
		PrivateImage: &tiktokpb.PrivateImageAttachment{
			Path:       protoString("tos-alisg-i-9aa6gs5p9y-sg/blob"),
			DecryptKey: protoString(decryptKey),
			Variants: []*tiktokpb.PrivateImageVariant{
				{
					Label:  []byte("view"),
					Url:    []string{thumbURL, thumbURL},
					Width:  protoUint64(808),
					Height: protoUint64(1796),
				},
				{
					Label:  []byte("full"),
					Url:    []string{fullURL, fullURL},
					Width:  protoUint64(808),
					Height: protoUint64(1796),
				},
			},
		},
	}

	msg, _, err := parseMessageEntry(ctx, nil, entry)
	if err != nil {
		t.Fatalf("parseMessageEntry returned error: %v", err)
	}
	if msg.Type != "image" {
		t.Fatalf("msg.Type = %q, want image", msg.Type)
	}
	if msg.Text != "[photo]" {
		t.Fatalf("msg.Text = %q, want [photo]", msg.Text)
	}
	if msg.MediaURL != fullURL {
		t.Fatalf("msg.MediaURL = %q, want %q", msg.MediaURL, fullURL)
	}
	if msg.ThumbnailURL != thumbURL {
		t.Fatalf("msg.ThumbnailURL = %q, want %q", msg.ThumbnailURL, thumbURL)
	}
	if msg.MediaDecryptKey != decryptKey {
		t.Fatalf("msg.MediaDecryptKey = %q, want %q", msg.MediaDecryptKey, decryptKey)
	}
	if msg.MediaWidth != 808 || msg.MediaHeight != 1796 {
		t.Fatalf("dimensions = %dx%d, want 808x1796", msg.MediaWidth, msg.MediaHeight)
	}
}

func TestParsePrivateImageFromWebsocketDetailProto(t *testing.T) {
	fullURL := "https://p0-tiktok-dm-private-sg.tiktok.com/tos-alisg-i-9aa6gs5p9y-sg/blob~tplv-9aa6gs5p9y-get:default.image?sig=full"
	thumbURL := "https://p0-tiktok-dm-private-sg.tiktok.com/tos-alisg-i-9aa6gs5p9y-sg/blob~tplv-9aa6gs5p9y-get:thumb.image?sig=thumb"
	decryptKey := "d1fa73054e2e8df4caebb81821d4ff39279419f0e2469df10acece30bcf6c69b"
	detail := &tiktokpb.WebsocketMessageDetail{
		ContentJson:    []byte(`{"hack":"1"}`),
		MessageSubtype: protoString("private_image"),
		PrivateImage: &tiktokpb.PrivateImageAttachment{
			DecryptKey: protoString(decryptKey),
			Variants: []*tiktokpb.PrivateImageVariant{
				{
					Label:  []byte{0x76, 0x69, 0x65, 0x77},
					Url:    []string{thumbURL},
					Width:  protoUint64(808),
					Height: protoUint64(1796),
				},
				{
					Label:  []byte("full"),
					Url:    []string{fullURL},
					Width:  protoUint64(808),
					Height: protoUint64(1796),
				},
			},
		},
	}

	msgType, mediaURL, thumb, key, width, height, durationMs, ok := parsePrivateMediaFromWebsocketDetailProto(detail)
	if !ok {
		t.Fatal("parsePrivateMediaFromWebsocketDetailProto returned ok=false")
	}
	if msgType != "image" {
		t.Fatalf("msgType = %q, want image", msgType)
	}
	if mediaURL != fullURL {
		t.Fatalf("mediaURL = %q, want %q", mediaURL, fullURL)
	}
	if thumb != thumbURL {
		t.Fatalf("thumb = %q, want %q", thumb, thumbURL)
	}
	if key != decryptKey {
		t.Fatalf("key = %q, want %q", key, decryptKey)
	}
	if width != 808 || height != 1796 {
		t.Fatalf("dimensions = %dx%d, want 808x1796", width, height)
	}
	if durationMs != 0 {
		t.Fatalf("durationMs = %d, want 0", durationMs)
	}
}

func TestParseMessageEntry_privateVideoOuterFields(t *testing.T) {
	ctx := context.Background()
	playURL := "https://webapp-sg.tiktok.com/private-video.mp4?mime_type=video_mp4&token=test-play"
	coverURL := "https://p0-tiktok-dm-private-sg.tiktokv.com/private-video-cover~tplv-noop.image?token=test-cover"
	entry := &tiktokpb.ConversationMessageEntry{
		ConversationId:  protoString("0:1:1000000000000000001:2000000000000000002"),
		ServerMessageId: protoUint64(8000000000000000003),
		TimestampUs:     protoUint64(1704067200123456),
		SenderUserId:    protoUint64(2000000000000000002),
		ContentJson:     []byte(`{"hack":"1"}`),
		MessageSubtype:  protoString("private_video"),
		PrivateImage: &tiktokpb.PrivateImageAttachment{
			Path: protoString("v0000testprivatevideo"),
			Variants: []*tiktokpb.PrivateImageVariant{
				{
					Label:        []byte("play"),
					Url:          []string{playURL, playURL},
					MetadataJson: protoString(`{"media_type":"video","video_id":"v0000testprivatevideo"}`),
					Width:        protoUint64(576),
					Height:       protoUint64(1024),
					DurationMs:   protoUint64(16835),
				},
				{
					Label:  []byte("cover"),
					Url:    []string{coverURL},
					Width:  protoUint64(576),
					Height: protoUint64(1024),
				},
			},
		},
	}

	msg, _, err := parseMessageEntry(ctx, nil, entry)
	if err != nil {
		t.Fatalf("parseMessageEntry returned error: %v", err)
	}
	if msg.Type != "video" {
		t.Fatalf("msg.Type = %q, want video", msg.Type)
	}
	if msg.MessageSubtype != "private_video" {
		t.Fatalf("msg.MessageSubtype = %q, want private_video", msg.MessageSubtype)
	}
	if msg.Text != "[video]" {
		t.Fatalf("msg.Text = %q, want [video]", msg.Text)
	}
	if msg.MediaURL != playURL {
		t.Fatalf("msg.MediaURL = %q, want %q", msg.MediaURL, playURL)
	}
	if msg.ThumbnailURL != coverURL {
		t.Fatalf("msg.ThumbnailURL = %q, want %q", msg.ThumbnailURL, coverURL)
	}
	if msg.MediaDurationMs != 16835 {
		t.Fatalf("msg.MediaDurationMs = %d, want 16835", msg.MediaDurationMs)
	}
	if msg.MediaWidth != 576 || msg.MediaHeight != 1024 {
		t.Fatalf("dimensions = %dx%d, want 576x1024", msg.MediaWidth, msg.MediaHeight)
	}
}

func TestParsePrivateVideoFromWebsocketDetailProto(t *testing.T) {
	playURL := "https://webapp-sg.tiktok.com/video.mp4?mime_type=video_mp4"
	coverURL := "https://p0-tiktok-dm-private-sg.tiktokv.com/cover~tplv-noop.image"
	detail := &tiktokpb.WebsocketMessageDetail{
		ContentJson:    []byte(`{"hack":"1"}`),
		MessageSubtype: protoString("private_video"),
		PrivateImage: &tiktokpb.PrivateImageAttachment{
			Variants: []*tiktokpb.PrivateImageVariant{
				{
					Label:        []byte("play"),
					Url:          []string{playURL},
					MetadataJson: protoString(`{"media_type":"video"}`),
					Width:        protoUint64(576),
					Height:       protoUint64(1024),
					DurationMs:   protoUint64(16835),
				},
				{
					Label:  []byte("cover"),
					Url:    []string{coverURL},
					Width:  protoUint64(576),
					Height: protoUint64(1024),
				},
			},
		},
	}

	msgType, mediaURL, thumb, key, width, height, durationMs, ok := parsePrivateMediaFromWebsocketDetailProto(detail)
	if !ok {
		t.Fatal("parsePrivateMediaFromWebsocketDetailProto returned ok=false")
	}
	if msgType != "video" {
		t.Fatalf("msgType = %q, want video", msgType)
	}
	if mediaURL != playURL {
		t.Fatalf("mediaURL = %q, want %q", mediaURL, playURL)
	}
	if thumb != coverURL {
		t.Fatalf("thumb = %q, want %q", thumb, coverURL)
	}
	if key != "" {
		t.Fatalf("key = %q, want empty", key)
	}
	if width != 576 || height != 1024 {
		t.Fatalf("dimensions = %dx%d, want 576x1024", width, height)
	}
	if durationMs != 16835 {
		t.Fatalf("durationMs = %d, want 16835", durationMs)
	}
}

func TestParseMessageEntry_replyMetadata(t *testing.T) {
	ctx := context.Background()
	parentID := uint64(8000000000000000001)
	entry := &tiktokpb.ConversationMessageEntry{
		ConversationId:  protoString("0:1:1234567890123456789:9876543210987654321"),
		ServerMessageId: protoUint64(8000000000000000002),
		TimestampUs:     protoUint64(1704067200000000),
		SenderUserId:    protoUint64(1234567890123456789),
		ContentJson:     []byte(`{"aweType":703,"text":"this is a reply"}`),
		CursorTsUs:      protoUint64(1704067100000000),
		MessageReply: &tiktokpb.MessageReplyReference{
			ReferencedServerMessageId: protoUint64(parentID),
			QuotedContextJson:         []byte(`{"refmsg_content":"{\"aweType\":0,\"text\":\"test\"}"}`),
		},
	}
	entry.Tags = []*tiktokpb.MetadataTag{
		{Key: protoString("s:client_message_id"), Value: []byte("00000000-0000-4000-8000-00000000aaaa")},
	}

	msg, _, _ := parseMessageEntry(ctx, nil, entry)
	if msg.Type != "text" || msg.Text != "this is a reply" {
		t.Fatalf("message body: type=%q text=%q", msg.Type, msg.Text)
	}
	if msg.ReplyToServerID != parentID {
		t.Fatalf("ReplyToServerID = %d, want %d", msg.ReplyToServerID, parentID)
	}
	if msg.ReplyQuotedText != "test" {
		t.Fatalf("ReplyQuotedText = %q, want test", msg.ReplyQuotedText)
	}
}
