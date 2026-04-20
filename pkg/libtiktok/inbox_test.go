package libtiktok

import (
	"context"
	"errors"
	"testing"

	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
)

const (
	syntheticDMUserA      = "1000000000000000001"
	syntheticDMUserB      = "1000000000000000002"
	syntheticGroupMemberC = "1000000000000000003"

	syntheticDMConversationID    = "0:1:" + syntheticDMUserA + ":" + syntheticDMUserB
	syntheticReplyConversationID = "0:1:1000000000000000004:1000000000000000005"
	syntheticLegacyDMConvID      = "0:1:111:222"

	syntheticGroupConversationID   = "2000000000000000001"
	syntheticSecondGroupConvID     = "2000000000000000002"
	syntheticGroupConversationName = "testing chat"
	syntheticSenderSecUID          = "synthetic-secuid-user-c"
	syntheticDeviceID              = "synthetic-device-id-0001"
)

func TestParseConversationEntryProto_DMConversationID(t *testing.T) {
	entry := &tiktokpb.InboxConversationEntry{
		ConversationId: protoString(syntheticDMConversationID),
		SourceId:       protoUint64(12345),
	}

	conv, err := parseConversationEntryProto(entry)
	if err != nil {
		t.Fatalf("parseConversationEntryProto returned error: %v", err)
	}
	if conv.ID != syntheticDMConversationID {
		t.Fatalf("conv.ID = %q", conv.ID)
	}
	if conv.SourceID != 12345 {
		t.Fatalf("conv.SourceID = %d", conv.SourceID)
	}
	if len(conv.Participants) != 2 || conv.Participants[0] != syntheticDMUserA || conv.Participants[1] != syntheticDMUserB {
		t.Fatalf("participants = %v", conv.Participants)
	}
}

func TestParseConversationEntryProto_GroupConversationID(t *testing.T) {
	entry := &tiktokpb.InboxConversationEntry{
		ConversationId:      protoString(syntheticGroupConversationID),
		ConversationType:    protoUint64(2),
		LastServerMessageId: protoUint64(3000000000000000001),
		SourceId:            protoUint64(2000000000000000001),
		LastMessageType:     protoUint64(7),
		LastMessagePreview:  []byte(`{"aweType":0,"rich_text_infos":[],"text":"Bots."}`),
	}

	conv, err := parseConversationEntryProto(entry)
	if err != nil {
		t.Fatalf("parseConversationEntryProto returned error: %v", err)
	}
	if conv.ID != syntheticGroupConversationID {
		t.Fatalf("conv.ID = %q", conv.ID)
	}
	if conv.SourceID != 2000000000000000001 {
		t.Fatalf("conv.SourceID = %d", conv.SourceID)
	}
	if len(conv.Participants) != 0 {
		t.Fatalf("participants = %v, want empty", conv.Participants)
	}
}

func TestParseConversationDetailProto_GroupConversation(t *testing.T) {
	detail := &tiktokpb.InboxConversationDetail{
		ConversationId:   protoString(syntheticGroupConversationID),
		SourceId:         protoUint64(2000000000000000001),
		ConversationType: protoUint64(2),
		Core: &tiktokpb.InboxConversationCore{
			Title: protoString(syntheticGroupConversationName),
		},
		Members: &tiktokpb.InboxConversationMembers{
			Entries: []*tiktokpb.InboxConversationMember{
				{UserId: protoUint64(1000000000000000001)},
				{UserId: protoUint64(1000000000000000002)},
				{UserId: protoUint64(1000000000000000003)},
			},
		},
	}

	conv, err := parseConversationDetailProto(detail)
	if err != nil {
		t.Fatalf("parseConversationDetailProto returned error: %v", err)
	}
	if conv.ID != syntheticGroupConversationID {
		t.Fatalf("conv.ID = %q", conv.ID)
	}
	if conv.SourceID != 2000000000000000001 {
		t.Fatalf("conv.SourceID = %d", conv.SourceID)
	}
	if conv.Name != syntheticGroupConversationName {
		t.Fatalf("conv.Name = %q, want %s", conv.Name, syntheticGroupConversationName)
	}
	want := []string{syntheticDMUserA, syntheticDMUserB, syntheticGroupMemberC}
	if len(conv.Participants) != len(want) {
		t.Fatalf("participants = %v, want %v", conv.Participants, want)
	}
	for i, got := range conv.Participants {
		if got != want[i] {
			t.Fatalf("participants[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func TestParseConversationDetailProto_MutedConversationState(t *testing.T) {
	detail := &tiktokpb.InboxConversationDetail{
		ConversationId:   protoString(syntheticGroupConversationID),
		SourceId:         protoUint64(2000000000000000001),
		ConversationType: protoUint64(2),
		State: &tiktokpb.InboxConversationState{
			Attributes: []*tiktokpb.MetadataKV{
				{Key: protoString("a:conv_set_notification"), Value: protoString("2")},
			},
		},
	}

	conv, err := parseConversationDetailProto(detail)
	if err != nil {
		t.Fatalf("parseConversationDetailProto returned error: %v", err)
	}
	if conv.Muted == nil || !*conv.Muted {
		t.Fatalf("conv.Muted = %v, want true", conv.Muted)
	}
}

func TestParseInboxResponse_GroupSummaryEntry(t *testing.T) {
	body := mustMarshalProto(t, &tiktokpb.InboxResponse{
		MessageType: protoUint64(203),
		SubCommand:  protoUint64(10002),
		Status:      protoUint64(0),
		Message:     protoString("OK"),
		Reserved_5:  protoUint64(1),
		Payload: &tiktokpb.InboxResponsePayload{
			UserInitList: &tiktokpb.InboxConversationList{
				Entries: []*tiktokpb.InboxConversationEntry{
					{
						ConversationId:         protoString(syntheticGroupConversationID),
						ConversationType:       protoUint64(2),
						LastServerMessageId:    protoUint64(3000000000000000001),
						LastMessageTimestampUs: protoUint64(1700000000000001),
						SourceId:               protoUint64(2000000000000000001),
						LastMessageType:        protoUint64(7),
						LastSenderUserId:       protoUint64(1000000000000000003),
						LastMessagePreview:     []byte(`{"aweType":0,"rich_text_infos":[],"text":"Bots."}`),
						LastSenderSecUid:       protoString(syntheticSenderSecUID),
						MessageSubtype:         protoString(""),
						CursorTsUs:             protoUint64(1699999999999000),
					},
				},
			},
		},
	})

	convs, err := parseInboxResponse(body)
	if err != nil {
		t.Fatalf("parseInboxResponse returned error: %v", err)
	}
	if len(convs) != 1 {
		t.Fatalf("len(convs) = %d, want 1", len(convs))
	}
	if convs[0].ID != syntheticGroupConversationID {
		t.Fatalf("convs[0].ID = %q", convs[0].ID)
	}
	if convs[0].SourceID != 2000000000000000001 {
		t.Fatalf("convs[0].SourceID = %d", convs[0].SourceID)
	}
	if len(convs[0].Participants) != 0 {
		t.Fatalf("convs[0].Participants = %v, want empty", convs[0].Participants)
	}
}

func TestParseInboxResponse_PrefersConversationDetails(t *testing.T) {
	body := mustMarshalProto(t, &tiktokpb.InboxResponse{
		MessageType: protoUint64(203),
		SubCommand:  protoUint64(10002),
		Status:      protoUint64(0),
		Message:     protoString("OK"),
		Reserved_5:  protoUint64(1),
		Payload: &tiktokpb.InboxResponsePayload{
			UserInitList: &tiktokpb.InboxConversationList{
				Entries: []*tiktokpb.InboxConversationEntry{
					{
						ConversationId:      protoString(syntheticGroupConversationID),
						ConversationType:    protoUint64(2),
						LastServerMessageId: protoUint64(3000000000000000001),
						SourceId:            protoUint64(2000000000000000001),
						LastMessageType:     protoUint64(7),
						LastMessagePreview:  []byte(`{"aweType":0,"text":"Bots."}`),
					},
					{
						ConversationId:      protoString(syntheticGroupConversationID),
						ConversationType:    protoUint64(2),
						LastServerMessageId: protoUint64(3000000000000000002),
						SourceId:            protoUint64(2000000000000000001),
						LastMessageType:     protoUint64(7),
						LastMessagePreview:  []byte(`{"aweType":700,"text":"h"}`),
					},
				},
				Conversations: []*tiktokpb.InboxConversationDetail{
					{
						ConversationId:   protoString(syntheticGroupConversationID),
						SourceId:         protoUint64(2000000000000000001),
						ConversationType: protoUint64(2),
						Core: &tiktokpb.InboxConversationCore{
							Title: protoString(syntheticGroupConversationName),
						},
						Members: &tiktokpb.InboxConversationMembers{
							Entries: []*tiktokpb.InboxConversationMember{
								{UserId: protoUint64(1000000000000000001)},
								{UserId: protoUint64(1000000000000000002)},
								{UserId: protoUint64(1000000000000000003)},
							},
						},
					},
					{
						ConversationId:   protoString(syntheticSecondGroupConvID),
						SourceId:         protoUint64(2000000000000000002),
						ConversationType: protoUint64(2),
						Members: &tiktokpb.InboxConversationMembers{
							Entries: []*tiktokpb.InboxConversationMember{
								{UserId: protoUint64(111)},
								{UserId: protoUint64(222)},
							},
						},
					},
				},
			},
		},
	})

	convs, err := parseInboxResponse(body)
	if err != nil {
		t.Fatalf("parseInboxResponse returned error: %v", err)
	}
	if len(convs) != 2 {
		t.Fatalf("len(convs) = %d, want 2", len(convs))
	}
	if convs[0].ID != syntheticGroupConversationID || convs[1].ID != syntheticSecondGroupConvID {
		t.Fatalf("conversation IDs = [%q, %q]", convs[0].ID, convs[1].ID)
	}
	if convs[0].Name != syntheticGroupConversationName {
		t.Fatalf("convs[0].Name = %q, want %s", convs[0].Name, syntheticGroupConversationName)
	}
}

func TestParseInboxResponse_MergesDetailAndLegacyEntries(t *testing.T) {
	body := mustMarshalProto(t, &tiktokpb.InboxResponse{
		MessageType: protoUint64(203),
		SubCommand:  protoUint64(10006),
		Status:      protoUint64(0),
		Message:     protoString("OK"),
		Reserved_5:  protoUint64(1),
		Payload: &tiktokpb.InboxResponsePayload{
			UserInitList: &tiktokpb.InboxConversationList{
				Entries: []*tiktokpb.InboxConversationEntry{
					{
						ConversationId:     protoString(syntheticLegacyDMConvID),
						SourceId:           protoUint64(222),
						LastMessagePreview: []byte(`{"aweType":700,"text":"dm"}`),
					},
				},
				Conversations: []*tiktokpb.InboxConversationDetail{
					{
						ConversationId:   protoString(syntheticGroupConversationID),
						SourceId:         protoUint64(2000000000000000001),
						ConversationType: protoUint64(2),
						Members: &tiktokpb.InboxConversationMembers{
							Entries: []*tiktokpb.InboxConversationMember{
								{UserId: protoUint64(1000000000000000001)},
								{UserId: protoUint64(1000000000000000002)},
							},
						},
					},
				},
			},
		},
	})

	convs, err := parseInboxResponse(body)
	if err != nil {
		t.Fatalf("parseInboxResponse returned error: %v", err)
	}
	if len(convs) != 2 {
		t.Fatalf("len(convs) = %d, want 2", len(convs))
	}
	if convs[0].ID != syntheticGroupConversationID || convs[1].ID != syntheticLegacyDMConvID {
		t.Fatalf("conversation IDs = [%q, %q]", convs[0].ID, convs[1].ID)
	}
}

func TestMergeInboxConversations(t *testing.T) {
	merged := mergeInboxConversations(
		[]Conversation{
			{
				ID:           "group-1",
				SourceID:     1,
				Participants: []string{"111", "222"},
				Muted:        boolPtr(false),
			},
		},
		[]Conversation{
			{
				ID:           "group-1",
				SourceID:     1,
				Participants: []string{"222", "333"},
				Name:         "testing chat",
				Muted:        boolPtr(true),
			},
			{
				ID:           "dm-1",
				SourceID:     2,
				Participants: []string{"444"},
			},
		},
	)

	if len(merged) != 2 {
		t.Fatalf("len(merged) = %d, want 2", len(merged))
	}
	if got := merged[0].Participants; len(got) != 3 || got[0] != "111" || got[1] != "222" || got[2] != "333" {
		t.Fatalf("merged[0].Participants = %v", got)
	}
	if merged[0].Name != "testing chat" {
		t.Fatalf("merged[0].Name = %q, want testing chat", merged[0].Name)
	}
	if merged[0].Muted == nil || !*merged[0].Muted {
		t.Fatalf("merged[0].Muted = %v, want true", merged[0].Muted)
	}
	if merged[1].ID != "dm-1" {
		t.Fatalf("merged[1].ID = %q", merged[1].ID)
	}
}

func TestBuildInboxPayloadMatchesObservedGroupChatVariant(t *testing.T) {
	payload, err := buildInboxPayload(syntheticDeviceID, "ms-token", "verify-fp", 10002)
	if err != nil {
		t.Fatalf("buildInboxPayload: %v", err)
	}

	var req tiktokpb.InboxRequest
	if err := unmarshalProto(payload, &req); err != nil {
		t.Fatalf("unmarshal inbox payload: %v", err)
	}

	if req.GetMessageType() != 203 {
		t.Fatalf("message_type = %d, want 203", req.GetMessageType())
	}
	if req.GetSubCommand() != 10002 {
		t.Fatalf("sub_command = %d, want 10002", req.GetSubCommand())
	}
	if req.GetReserved_6() != 1 {
		t.Fatalf("reserved_6 = %d, want 1", req.GetReserved_6())
	}
	init := req.GetPayload().GetUserInitList()
	if init.GetSortType() != 0 || init.GetCursor() != 0 || init.GetConType() != 0 || init.GetLimit() != 0 {
		t.Fatalf("payload.user_init_list = sort_type %d cursor %d con_type %d limit %d, want minimal 0,0,0,0",
			init.GetSortType(), init.GetCursor(), init.GetConType(), init.GetLimit())
	}
	if req.GetDeviceId() != syntheticDeviceID {
		t.Fatalf("device_id = %q", req.GetDeviceId())
	}

	metadata := req.GetMetadata()
	if got := metadataValue(metadata, "referer"); got != "https://www.tiktok.com/messages" {
		t.Fatalf("referer = %q", got)
	}
	if got := metadataValue(metadata, "browser_version"); got != DefaultUserAgent {
		t.Fatalf("browser_version = %q", got)
	}
	if got := metadataValue(metadata, "user_agent"); got != DefaultUserAgent {
		t.Fatalf("user_agent = %q", got)
	}
	if got := metadataValue(metadata, "verifyFp"); got != "verify-fp" {
		t.Fatalf("verifyFp = %q", got)
	}
	if got := metadataValue(metadata, "Web-Sdk-Ms-Token"); got != "ms-token" {
		t.Fatalf("Web-Sdk-Ms-Token = %q", got)
	}
}

func TestBuildInboxPayloadSupportsNormalConversationVariant(t *testing.T) {
	payload, err := buildInboxPayload(syntheticDeviceID, "ms-token", "verify-fp", 10006)
	if err != nil {
		t.Fatalf("buildInboxPayload: %v", err)
	}

	var req tiktokpb.InboxRequest
	if err := unmarshalProto(payload, &req); err != nil {
		t.Fatalf("unmarshal inbox payload: %v", err)
	}
	if req.GetSubCommand() != 10006 {
		t.Fatalf("sub_command = %d, want 10006", req.GetSubCommand())
	}
	if req.GetReserved_6() != 0 {
		t.Fatalf("reserved_6 = %d, want 0", req.GetReserved_6())
	}
}

func TestBuildMetadataPreservesObservedInboxOrdering(t *testing.T) {
	pairs := buildMetadata("device-id", "ms-token", "verify-fp")
	keys := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		keys = append(keys, pair.k)
	}

	verifyIdx := indexOf(keys, "verifyFp")
	appLangIdx := indexOf(keys, "app_language")
	userAgentIdx := indexOf(keys, "user_agent")
	msTokenIdx := indexOf(keys, "Web-Sdk-Ms-Token")
	if verifyIdx == -1 || appLangIdx == -1 || userAgentIdx == -1 || msTokenIdx == -1 {
		t.Fatalf("expected verifyFp/app_language/user_agent/Web-Sdk-Ms-Token metadata keys, got %v", keys)
	}
	if verifyIdx > appLangIdx {
		t.Fatalf("verifyFp should precede app_language, got keys %v", keys)
	}
	if msTokenIdx != len(keys)-1 {
		t.Fatalf("Web-Sdk-Ms-Token should be last, got keys %v", keys)
	}
	if userAgentIdx > msTokenIdx {
		t.Fatalf("user_agent should precede Web-Sdk-Ms-Token, got keys %v", keys)
	}
}

func metadataValue(metadata []*tiktokpb.MetadataKV, key string) string {
	for _, pair := range metadata {
		if pair.GetKey() == key {
			return pair.GetValue()
		}
	}
	return ""
}

func boolPtr(v bool) *bool {
	return &v
}

func indexOf(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return -1
}

func TestParseReplyQuotedTextFromWire(t *testing.T) {
	raw := `{"content":"{\"aweType\":0,\"text\":\"test\"}","refmsg_content":"{\"aweType\":0,\"text\":\"test\"}","refmsg_uid":"1000000000000000006"}`
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

func TestParseMessageEntry_skipRecalledOrInvisible(t *testing.T) {
	ctx := context.Background()
	const wantCursor uint64 = 1699999999999000
	entry := &tiktokpb.ConversationMessageEntry{
		ConversationId:  protoString(syntheticDMConversationID),
		ServerMessageId: protoUint64(1),
		TimestampUs:     protoUint64(1700000000000000),
		SenderUserId:    protoUint64(1000000000000000007),
		ContentJson:     []byte(`{"aweType":0,"text":"should not appear"}`),
		CursorTsUs:      protoUint64(wantCursor),
		Tags: []*tiktokpb.MetadataTag{
			{Key: protoString("s:is_recalled"), Value: []byte("1")},
		},
	}
	_, cursor, err := parseMessageEntry(ctx, nil, entry)
	if !errors.Is(err, errSkipSyncedMessage) {
		t.Fatalf("parseMessageEntry err = %v, want errSkipSyncedMessage", err)
	}
	if cursor != wantCursor {
		t.Fatalf("cursor = %d, want %d", cursor, wantCursor)
	}

	const wantCursor2 uint64 = 1699999999998000
	entry2 := &tiktokpb.ConversationMessageEntry{
		ConversationId:  protoString(syntheticDMConversationID),
		ServerMessageId: protoUint64(2),
		TimestampUs:     protoUint64(1700000000000000),
		SenderUserId:    protoUint64(1000000000000000007),
		ContentJson:     []byte(`{"aweType":0,"text":"hidden"}`),
		CursorTsUs:      protoUint64(wantCursor2),
		Tags: []*tiktokpb.MetadataTag{
			{Key: protoString("s:invisible"), Value: []byte("1")},
		},
	}
	_, cursor2, err2 := parseMessageEntry(ctx, nil, entry2)
	if !errors.Is(err2, errSkipSyncedMessage) {
		t.Fatalf("parseMessageEntry (s:invisible tag) err = %v, want errSkipSyncedMessage", err2)
	}
	if cursor2 != wantCursor2 {
		t.Fatalf("cursor2 = %d, want %d", cursor2, wantCursor2)
	}

	entry3 := &tiktokpb.ConversationMessageEntry{
		ConversationId:  protoString(syntheticDMConversationID),
		ServerMessageId: protoUint64(3),
		TimestampUs:     protoUint64(1700000000000000),
		SenderUserId:    protoUint64(1000000000000000007),
		ContentJson:     []byte(`{"aweType":0,"text":"visible"}`),
		CursorTsUs:      protoUint64(1700000000000001),
		Tags: []*tiktokpb.MetadataTag{
			{Key: protoString("s:invisible"), Value: []byte("  ")},
		},
	}
	msg3, _, err3 := parseMessageEntry(ctx, nil, entry3)
	if err3 != nil {
		t.Fatalf("parseMessageEntry: %v", err3)
	}
	if msg3.Text != "visible" {
		t.Fatalf("text = %q, want visible", msg3.Text)
	}
}

func TestParseMessageEntry_stickerOuterFields(t *testing.T) {
	ctx := context.Background()
	url := "https://example.invalid/stickers/full-sticker.awebp"
	entry := &tiktokpb.ConversationMessageEntry{
		ConversationId:  protoString(syntheticDMConversationID),
		ServerMessageId: protoUint64(3000000000000000101),
		TimestampUs:     protoUint64(1700000000000101),
		SenderUserId:    protoUint64(1000000000000000007),
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
	url := "https://example.invalid/stickers/frame-sticker.awebp"
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
	fullURL := "https://example.invalid/media/private-image-full.image"
	thumbURL := "https://example.invalid/media/private-image-thumb.image"
	decryptKey := "1111111111111111111111111111111111111111111111111111111111111111"
	entry := &tiktokpb.ConversationMessageEntry{
		ConversationId:  protoString(syntheticDMConversationID),
		ServerMessageId: protoUint64(3000000000000000201),
		TimestampUs:     protoUint64(1700000000000201),
		SenderUserId:    protoUint64(1000000000000000007),
		ContentJson:     []byte(`{"hack":"1"}`),
		MessageSubtype:  protoString("private_image"),
		PrivateImage: &tiktokpb.PrivateImageAttachment{
			Path:       protoString("synthetic/private-image-blob"),
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
	fullURL := "https://example.invalid/media/private-image-full.image"
	thumbURL := "https://example.invalid/media/private-image-thumb.image"
	decryptKey := "1111111111111111111111111111111111111111111111111111111111111111"
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
		ConversationId:  protoString(syntheticReplyConversationID),
		ServerMessageId: protoUint64(8000000000000000002),
		TimestampUs:     protoUint64(1704067200000000),
		SenderUserId:    protoUint64(1000000000000000004),
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
