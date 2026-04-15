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

func TestParseMessageEntry_replyMetadata(t *testing.T) {
	ctx := context.Background()
	parentID := uint64(8000000000000000001)
	entry := &tiktokpb.ConversationMessageEntry{
		ConversationId:   protoString("0:1:1234567890123456789:9876543210987654321"),
		ServerMessageId:  protoUint64(8000000000000000002),
		TimestampUs:      protoUint64(1704067200000000),
		SenderUserId:     protoUint64(1234567890123456789),
		ContentJson:      []byte(`{"aweType":703,"text":"this is a reply"}`),
		CursorTsUs:       protoUint64(1704067100000000),
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
