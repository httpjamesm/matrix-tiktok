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
	if entry.GetLastMessageType() != 0 {
		return true
	}
	return false
}

func parseConversationEntryProto(entry *tiktokpb.InboxConversationEntry) (Conversation, error) {
	convID := entry.GetConversationId()
	sourceID := entry.GetSourceId()

	parts := strings.Split(convID, ":")
	if len(parts) < 2 {
		return Conversation{}, fmt.Errorf("unexpected convID format: %q", convID)
	}

	return Conversation{
		ID:           convID,
		SourceID:     sourceID,
		Participants: parts[len(parts)-2:],
	}, nil
}
