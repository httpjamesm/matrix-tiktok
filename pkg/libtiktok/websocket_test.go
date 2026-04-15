package libtiktok

import (
	"testing"

	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
)

func TestTryParseWSMessageDeletion(t *testing.T) {
	// Synthetic IDs only — message_id is >2^53 to assert JSON→uint64 does not
	// go through float64 and lose precision.
	const (
		wantID    uint64 = 9007199254740993 // 2^53 + 1
		senderUID uint64 = 1111111111111111111
		tsUs      uint64 = 1_640_000_000_000_000
		wantTsMs  int64  = 1_640_000_000_000
		convID           = "0:1:1000000000000000001:2000000000000000002"
	)
	jsonBody := `{"command_type":2,"conversation_id":"0:1:1000000000000000001:2000000000000000002","conversation_type":1,"inbox_type":0,"message_id":9007199254740993,"read_badge_count_v2":12}`

	chat := &tiktokpb.WebsocketChat{
		ConversationId: protoString(convID),
	}
	detail := &tiktokpb.WebsocketMessageDetail{
		ContentJson:  []byte(jsonBody),
		TimestampUs:  protoUint64(tsUs),
		SenderUserId: protoUint64(senderUID),
	}

	handled, evt := tryParseWSMessageDeletion(chat, detail)
	if !handled || evt == nil || evt.Deletion == nil {
		t.Fatalf("expected deletion event, got handled=%v evt=%v", handled, evt)
	}
	d := evt.Deletion
	if d.DeletedMessageID != wantID {
		t.Fatalf("DeletedMessageID: got %d want %d", d.DeletedMessageID, wantID)
	}
	if d.ConversationID != convID {
		t.Fatalf("ConversationID: got %q", d.ConversationID)
	}
	if d.DeleterUserID != "1111111111111111111" {
		t.Fatalf("DeleterUserID: got %q", d.DeleterUserID)
	}
	if d.TimestampMs != wantTsMs {
		t.Fatalf("TimestampMs: got %d want %d", d.TimestampMs, wantTsMs)
	}
}

func TestTryParseWSMessageDeletionFallsBackToChatConvID(t *testing.T) {
	jsonBody := `{"command_type":2,"message_id":42}`
	chat := &tiktokpb.WebsocketChat{ConversationId: protoString("0:1:a:b")}
	detail := &tiktokpb.WebsocketMessageDetail{ContentJson: []byte(jsonBody)}

	handled, evt := tryParseWSMessageDeletion(chat, detail)
	if !handled || evt == nil || evt.Deletion == nil {
		t.Fatal("expected deletion")
	}
	if evt.Deletion.ConversationID != "0:1:a:b" {
		t.Fatalf("got %q", evt.Deletion.ConversationID)
	}
}

func TestTryParseWSMessageDeletionNotACommand(t *testing.T) {
	detail := &tiktokpb.WebsocketMessageDetail{
		ContentJson: []byte(`{"aweType":700,"text":"hi"}`),
	}
	handled, evt := tryParseWSMessageDeletion(&tiktokpb.WebsocketChat{}, detail)
	if handled || evt != nil {
		t.Fatalf("expected not handled, got handled=%v evt=%v", handled, evt)
	}
}
