package libtiktok

import (
	"fmt"
	"strconv"
	"strings"

	tiktokpb "github.com/httpjamesm/matrix-tiktok/pkg/libtiktok/pb"
	"google.golang.org/protobuf/proto"
)

func protoString(v string) *string {
	return &v
}

func protoUint64(v uint64) *uint64 {
	return &v
}

func protoInt32(v int32) *int32 {
	return &v
}

func protoInt64(v int64) *int64 {
	return &v
}

func emptyProtoMessage() *tiktokpb.EmptyMessage {
	return &tiktokpb.EmptyMessage{}
}

func mustMarshalProto(msg proto.Message) []byte {
	b, err := proto.Marshal(msg)
	if err != nil {
		panic(fmt.Sprintf("marshal protobuf: %v", err))
	}
	return b
}

func unmarshalProto(data []byte, msg proto.Message) error {
	return proto.Unmarshal(data, msg)
}

func metadataKVsToProto(pairs []metaKV) []*tiktokpb.MetadataKV {
	out := make([]*tiktokpb.MetadataKV, 0, len(pairs))
	for _, kv := range pairs {
		out = append(out, &tiktokpb.MetadataKV{
			Key:   protoString(kv.k),
			Value: protoString(kv.v),
		})
	}
	return out
}

func extractClientMsgIDFromTags(tags []*tiktokpb.MetadataTag) string {
	for _, tag := range tags {
		if tag.GetKey() == "s:client_message_id" && len(tag.GetValue()) > 0 {
			return string(tag.GetValue())
		}
	}
	return ""
}

// shouldSkipSyncedMessage reports whether a get_by_conversation row or WS chat
// detail is a recalled or invisible placeholder. TikTok carries these on field 9
// (repeated MetadataTag tags).
func shouldSkipSyncedMessage(tags []*tiktokpb.MetadataTag) bool {
	for _, tag := range tags {
		if tag == nil {
			continue
		}
		switch tag.GetKey() {
		case "s:is_recalled":
			if strings.TrimSpace(string(tag.GetValue())) == "1" {
				return true
			}
		case "s:invisible":
			if strings.TrimSpace(string(tag.GetValue())) != "" {
				return true
			}
		}
	}
	return false
}

func parseReactionsProto(entries []*tiktokpb.ReactionSummary) []Reaction {
	if len(entries) == 0 {
		return nil
	}

	out := make([]Reaction, 0, len(entries))
	for _, entry := range entries {
		emoji := strings.TrimPrefix(entry.GetReactionKey(), "e:")
		if emoji == "" {
			continue
		}

		userEntries := entry.GetUsers().GetEntries()
		userIDs := make([]string, 0, len(userEntries))
		for _, user := range userEntries {
			if uid := user.GetUserIdStr(); uid != "" {
				userIDs = append(userIDs, uid)
			} else if uid := user.GetUserId(); uid != 0 {
				userIDs = append(userIDs, strconv.FormatUint(uid, 10))
			}
		}

		out = append(out, Reaction{Emoji: emoji, UserIDs: userIDs})
	}
	return deduplicateReactions(out)
}

func hasRealMessageProto(entry *tiktokpb.InboxConversationEntry) bool {
	raw := entry.GetLastMessagePreview()
	if len(raw) > 0 && !strings.EqualFold(strings.TrimSpace(string(raw)), "placeholder") {
		return true
	}
	if entry.GetSourceId() != 0 {
		return true
	}
	if entry.GetLastServerMessageId() != 0 {
		return true
	}
	if entry.GetLastMessageType() != 0 {
		return true
	}
	return false
}

func parseConversationEntryProto(entry *tiktokpb.InboxConversationEntry) (Conversation, error) {
	convID := entry.GetConversationId()
	sourceID := entry.GetSourceId()
	if convID == "" {
		return Conversation{}, fmt.Errorf("missing conversation ID")
	}

	participants := []string(nil)
	if strings.Contains(convID, ":") {
		parts := strings.Split(convID, ":")
		if len(parts) < 2 {
			return Conversation{}, fmt.Errorf("unexpected convID format: %q", convID)
		}
		participants = parts[len(parts)-2:]
	}

	return Conversation{
		ID:               convID,
		SourceID:         sourceID,
		Participants:     participants,
		ConversationType: entry.GetConversationType(),
	}, nil
}

func parseConversationDetailProto(detail *tiktokpb.InboxConversationDetail) (Conversation, error) {
	convID := detail.GetConversationId()
	if convID == "" {
		return Conversation{}, fmt.Errorf("missing conversation ID")
	}

	sourceID := detail.GetSourceId()
	if sourceID == 0 {
		sourceID = detail.GetCore().GetSourceId()
	}

	conversationType := detail.GetConversationType()
	if conversationType == 0 {
		conversationType = detail.GetCore().GetConversationType()
	}
	name := detail.GetCore().GetTitle()

	participants := make([]string, 0, len(detail.GetMembers().GetEntries()))
	for _, member := range detail.GetMembers().GetEntries() {
		if uid := member.GetUserId(); uid != 0 {
			participants = append(participants, strconv.FormatUint(uid, 10))
		}
	}

	if len(participants) == 0 && strings.Contains(convID, ":") {
		parts := strings.Split(convID, ":")
		if len(parts) >= 2 {
			participants = append(participants, parts[len(parts)-2:]...)
		}
	}

	return Conversation{
		ID:               convID,
		SourceID:         sourceID,
		Participants:     participants,
		Name:             name,
		ConversationType: conversationType,
	}, nil
}
