package libtiktok

import (
	"context"
	"testing"

	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
)

func TestTryParseWSDeleteForSelf(t *testing.T) {
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

	handled, evt := tryParseWSDeleteForSelf(chat, detail)
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
	if !d.OnlyForMe {
		t.Fatal("expected OnlyForMe=true")
	}
}

func TestTryParseWSDeleteForSelfFallsBackToChatConvID(t *testing.T) {
	jsonBody := `{"command_type":2,"message_id":42}`
	chat := &tiktokpb.WebsocketChat{ConversationId: protoString("0:1:a:b")}
	detail := &tiktokpb.WebsocketMessageDetail{ContentJson: []byte(jsonBody)}

	handled, evt := tryParseWSDeleteForSelf(chat, detail)
	if !handled || evt == nil || evt.Deletion == nil {
		t.Fatal("expected deletion")
	}
	if evt.Deletion.ConversationID != "0:1:a:b" {
		t.Fatalf("got %q", evt.Deletion.ConversationID)
	}
}

func TestTryParseWSDeleteForSelfNotACommand(t *testing.T) {
	detail := &tiktokpb.WebsocketMessageDetail{
		ContentJson: []byte(`{"aweType":700,"text":"hi"}`),
	}
	handled, evt := tryParseWSDeleteForSelf(&tiktokpb.WebsocketChat{}, detail)
	if handled || evt != nil {
		t.Fatalf("expected not handled, got handled=%v evt=%v", handled, evt)
	}
}

func TestTryParseWSDeleteForEveryone(t *testing.T) {
	const (
		convID       = "0:1:1111111111111111111:2222222222222222222"
		deletedMsgID = uint64(7628835564771083797)
		recallUID    = "1111111111111111111"
		senderUID    = uint64(1111111111111111111)
		tsUs         = uint64(1776226702263000)
		wantTsMs     = int64(1776226702263)
	)

	chat := &tiktokpb.WebsocketChat{
		ConversationId: protoString(convID),
	}
	detail := &tiktokpb.WebsocketMessageDetail{
		ServerMessageId: protoUint64(7628835586388461575), // wrapper event message ID, not the recalled target
		TimestampUs:     protoUint64(tsUs),
		SenderUserId:    protoUint64(senderUID),
		Tags: []*tiktokpb.MetadataTag{
			{Key: protoString("s:client_message_id"), Value: []byte("2a0cecfb-d01c-4b6c-b9b4-381ecdd905a4")},
			{Key: protoString("a:disable_outer_push"), Value: []byte("1")},
			{Key: protoString("s:server_message_id"), Value: []byte("7628835564771083797")},
			{Key: protoString("s:recall_uid"), Value: []byte(recallUID)},
		},
	}

	handled, evt := tryParseWSDeleteForEveryone(chat, detail)
	if !handled || evt == nil || evt.Deletion == nil {
		t.Fatalf("expected delete-for-everyone event, got handled=%v evt=%v", handled, evt)
	}
	d := evt.Deletion
	if d.ConversationID != convID {
		t.Fatalf("ConversationID: got %q", d.ConversationID)
	}
	if d.DeletedMessageID != deletedMsgID {
		t.Fatalf("DeletedMessageID: got %d want %d", d.DeletedMessageID, deletedMsgID)
	}
	if d.DeleterUserID != recallUID {
		t.Fatalf("DeleterUserID: got %q want %q", d.DeleterUserID, recallUID)
	}
	if d.TimestampMs != wantTsMs {
		t.Fatalf("TimestampMs: got %d want %d", d.TimestampMs, wantTsMs)
	}
	if d.OnlyForMe {
		t.Fatal("expected OnlyForMe=false")
	}
}

func TestParseReadReceiptEvent(t *testing.T) {
	const (
		convID = "0:1:1000000000000000001:2000000000000000002"
		peerID = uint64(2000000000000000002)
		msgID  = uint64(9007199254740994)
		tsUs   = uint64(1_640_000_000_000_000)
	)
	reserved2 := uint32(1)
	innerType := uint64(501)
	env := &tiktokpb.WebsocketEnvelope{
		InnerType: &innerType,
		Commands: &tiktokpb.WebsocketCommands{
			ReadReceipt: &tiktokpb.WebsocketReadReceipt{
				ConversationId:      protoString(convID),
				Reserved_2:          &reserved2,
				ReadTimestampUs:     protoUint64(tsUs),
				PeerOrInboxId:       protoUint64(peerID),
				ReadServerMessageId: protoUint64(msgID),
			},
		},
	}

	evt, err := parseReadReceiptEvent(context.Background(), env)
	if err != nil {
		t.Fatalf("parseReadReceiptEvent: %v", err)
	}
	if evt == nil || evt.ReadReceipt == nil {
		t.Fatalf("expected read receipt event, got evt=%v", evt)
	}
	rr := evt.ReadReceipt
	if rr.ConversationID != convID {
		t.Fatalf("ConversationID: got %q", rr.ConversationID)
	}
	if rr.ReadServerMessageID != msgID {
		t.Fatalf("ReadServerMessageID: got %d want %d", rr.ReadServerMessageID, msgID)
	}
	if rr.ReadTimestampUs != tsUs {
		t.Fatalf("ReadTimestampUs: got %d want %d", rr.ReadTimestampUs, tsUs)
	}
	if rr.ReaderUserID != "2000000000000000002" {
		t.Fatalf("ReaderUserID: got %q", rr.ReaderUserID)
	}
	if rr.Reserved2 != 1 {
		t.Fatalf("Reserved2: got %d", rr.Reserved2)
	}
}

func TestParseReadReceiptEventNilWhenMissingReadReceipt(t *testing.T) {
	innerType := uint64(501)
	env := &tiktokpb.WebsocketEnvelope{
		InnerType: &innerType,
		Commands:  &tiktokpb.WebsocketCommands{},
	}
	evt, err := parseReadReceiptEvent(context.Background(), env)
	if err != nil {
		t.Fatalf("parseReadReceiptEvent: %v", err)
	}
	if evt != nil {
		t.Fatalf("expected nil event, got %#v", evt)
	}
}

func TestParseReadReceiptEventNilWhenMissingConversationID(t *testing.T) {
	innerType := uint64(501)
	env := &tiktokpb.WebsocketEnvelope{
		InnerType: &innerType,
		Commands: &tiktokpb.WebsocketCommands{
			ReadReceipt: &tiktokpb.WebsocketReadReceipt{
				ReadServerMessageId: protoUint64(42),
				PeerOrInboxId:       protoUint64(7),
			},
		},
	}
	evt, err := parseReadReceiptEvent(context.Background(), env)
	if err != nil {
		t.Fatalf("parseReadReceiptEvent: %v", err)
	}
	if evt != nil {
		t.Fatalf("expected nil event, got %#v", evt)
	}
}

func TestParseReadReceiptEventReaderEmptyWhenPeerZero(t *testing.T) {
	innerType := uint64(501)
	convID := "0:1:1:2"
	env := &tiktokpb.WebsocketEnvelope{
		InnerType: &innerType,
		Commands: &tiktokpb.WebsocketCommands{
			ReadReceipt: &tiktokpb.WebsocketReadReceipt{
				ConversationId:      protoString(convID),
				ReadServerMessageId: protoUint64(99),
			},
		},
	}
	evt, err := parseReadReceiptEvent(context.Background(), env)
	if err != nil {
		t.Fatalf("parseReadReceiptEvent: %v", err)
	}
	if evt == nil || evt.ReadReceipt == nil {
		t.Fatal("expected event")
	}
	if evt.ReadReceipt.ReaderUserID != "" {
		t.Fatalf("ReaderUserID: got %q want empty", evt.ReadReceipt.ReaderUserID)
	}
}

func TestParseTypingIndicatorEvent(t *testing.T) {
	const (
		convID   = "0:1:1000000000000000001:2000000000000000002"
		senderID = uint64(1000000000000000001)
		sourceID = uint64(9007199254740997)
		createMs = uint64(1640000000123)
	)
	innerType := uint64(510)
	env := &tiktokpb.WebsocketEnvelope{
		InnerType: &innerType,
		Commands: &tiktokpb.WebsocketCommands{
			TypingIndicator: &tiktokpb.WebsocketTypingIndicator{
				SenderUserId:         protoUint64(senderID),
				ConversationId:       protoString(convID),
				ConversationSourceId: protoUint64(sourceID),
				Reserved_4:           protoUint64(1),
				Reserved_5:           protoUint64(3),
				Reserved_6:           protoUint64(0),
				CreateTimeMs:         protoUint64(createMs),
			},
		},
	}

	evt, err := parseTypingIndicatorEvent(context.Background(), env)
	if err != nil {
		t.Fatalf("parseTypingIndicatorEvent: %v", err)
	}
	if evt == nil || evt.Typing == nil {
		t.Fatalf("expected typing event, got %#v", evt)
	}
	ti := evt.Typing
	if ti.ConversationID != convID {
		t.Fatalf("ConversationID: got %q want %q", ti.ConversationID, convID)
	}
	if ti.SenderUserID != "1000000000000000001" {
		t.Fatalf("SenderUserID: got %q", ti.SenderUserID)
	}
	if ti.ConversationSourceID != sourceID {
		t.Fatalf("ConversationSourceID: got %d want %d", ti.ConversationSourceID, sourceID)
	}
	if ti.CreateTimeMs != createMs {
		t.Fatalf("CreateTimeMs: got %d want %d", ti.CreateTimeMs, createMs)
	}
}

func TestParseTypingIndicatorEventNilWhenMissingRequiredFields(t *testing.T) {
	innerType := uint64(510)
	env := &tiktokpb.WebsocketEnvelope{
		InnerType: &innerType,
		Commands: &tiktokpb.WebsocketCommands{
			TypingIndicator: &tiktokpb.WebsocketTypingIndicator{
				CreateTimeMs: protoUint64(1640000000123),
			},
		},
	}

	evt, err := parseTypingIndicatorEvent(context.Background(), env)
	if err != nil {
		t.Fatalf("parseTypingIndicatorEvent: %v", err)
	}
	if evt != nil {
		t.Fatalf("expected nil event, got %#v", evt)
	}
}
