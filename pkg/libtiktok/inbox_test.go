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
