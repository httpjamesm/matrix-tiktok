package connector

import (
	"context"
	"testing"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const (
	syntheticSelfUserID   = "2000000000000000002"
	syntheticOtherUserID  = "1000000000000000001"
	syntheticDMConvID     = "0:1:" + syntheticOtherUserID + ":" + syntheticSelfUserID
	syntheticConvSourceID = 9007199254740992
	syntheticGroupConvID  = "9000000000000000000"
)

func TestGetChatInfo_UsesStoredGroupName(t *testing.T) {
	tc := &TikTokClient{
		meta:       &UserLoginMetadata{UserID: syntheticSelfUserID},
		otherUsers: map[string]string{},
		groupNames: map[string]string{},
	}
	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey: networkid.PortalKey{
				ID: makePortalID(syntheticGroupConvID),
			},
			Metadata: &PortalMetadata{
				ConversationID:   syntheticGroupConvID,
				ConversationType: 2,
				GroupName:        "testing chat",
			},
		},
	}

	info, err := tc.GetChatInfo(context.Background(), portal)
	if err != nil {
		t.Fatalf("GetChatInfo returned error: %v", err)
	}
	if info.Name == nil || *info.Name != "testing chat" {
		t.Fatalf("info.Name = %v, want testing chat", info.Name)
	}
	if info.Members == nil || len(info.Members.Members) != 1 {
		t.Fatalf("info.Members = %+v, want only self member for group chats without a peer cache", info.Members)
	}
	if !info.Members.Members[0].IsFromMe {
		t.Fatalf("first member = %+v, want self member", info.Members.Members[0])
	}
	if info.Type != nil {
		t.Fatalf("info.Type = %v, want nil for group chat", *info.Type)
	}
}

func TestBuildGroupChatInfo_PersistsPortalMetadata(t *testing.T) {
	tc := &TikTokClient{}
	conv := &libtiktok.Conversation{
		ID:               syntheticGroupConvID,
		SourceID:         9000000000000000001,
		Name:             "testing chat",
		ConversationType: 2,
	}

	info := tc.buildGroupChatInfo(conv)
	if info == nil || info.Name == nil || *info.Name != "testing chat" {
		t.Fatalf("info = %+v, want named group chat info", info)
	}

	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey: networkid.PortalKey{
				ID: makePortalID(conv.ID),
			},
		},
	}
	changed := info.ExtraUpdates(context.Background(), portal)
	if !changed {
		t.Fatal("ExtraUpdates reported no change")
	}

	meta, ok := portal.Metadata.(*PortalMetadata)
	if !ok || meta == nil {
		t.Fatalf("portal.Metadata = %#v, want *PortalMetadata", portal.Metadata)
	}
	if meta.ConversationID != conv.ID || meta.SourceID != conv.SourceID || meta.ConversationType != conv.ConversationType || meta.GroupName != conv.Name {
		t.Fatalf("portal metadata = %+v", meta)
	}
}

func TestUpdatePortalMetadata_DMSetsRoomTypeAndOtherUser(t *testing.T) {
	tc := &TikTokClient{
		meta:       &UserLoginMetadata{UserID: syntheticSelfUserID},
		otherUsers: map[string]string{},
		groupNames: map[string]string{},
	}
	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey: networkid.PortalKey{
				ID: makePortalID(syntheticDMConvID),
			},
		},
	}
	conv := &libtiktok.Conversation{
		ID:               syntheticDMConvID,
		SourceID:         syntheticConvSourceID,
		Participants:     []string{syntheticOtherUserID, syntheticSelfUserID},
		ConversationType: 1,
	}

	changed := tc.updatePortalMetadata(portal, conv)
	if !changed {
		t.Fatal("updatePortalMetadata reported no change")
	}
	meta, ok := portal.Metadata.(*PortalMetadata)
	if !ok || meta == nil {
		t.Fatalf("portal.Metadata = %#v, want *PortalMetadata", portal.Metadata)
	}
	if meta.ConversationID != conv.ID || meta.SourceID != conv.SourceID || meta.ConversationType != conv.ConversationType {
		t.Fatalf("portal metadata = %+v", meta)
	}
	if portal.RoomType != database.RoomTypeDM {
		t.Fatalf("portal.RoomType = %q, want dm", portal.RoomType)
	}
	if portal.OtherUserID != networkid.UserID(syntheticOtherUserID) {
		t.Fatalf("portal.OtherUserID = %q", portal.OtherUserID)
	}
}

func TestGetChatInfo_DMUsesPersistedMetadata(t *testing.T) {
	tc := &TikTokClient{
		meta:       &UserLoginMetadata{UserID: syntheticSelfUserID},
		otherUsers: map[string]string{},
		groupNames: map[string]string{},
	}
	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey: networkid.PortalKey{
				ID: makePortalID(syntheticDMConvID),
			},
			OtherUserID: networkid.UserID(syntheticOtherUserID),
			RoomType:    database.RoomTypeDM,
			Metadata: &PortalMetadata{
				ConversationID:   syntheticDMConvID,
				SourceID:         syntheticConvSourceID,
				ConversationType: 1,
			},
		},
	}

	info, err := tc.GetChatInfo(context.Background(), portal)
	if err != nil {
		t.Fatalf("GetChatInfo returned error: %v", err)
	}
	if info.Type == nil || *info.Type != database.RoomTypeDM {
		t.Fatalf("info.Type = %v, want dm", info.Type)
	}
	if info.Members == nil {
		t.Fatal("info.Members is nil")
	}
	if info.Members.OtherUserID != networkid.UserID(syntheticOtherUserID) {
		t.Fatalf("info.Members.OtherUserID = %q", info.Members.OtherUserID)
	}
	if len(info.Members.Members) != 2 {
		t.Fatalf("len(info.Members.Members) = %d, want 2", len(info.Members.Members))
	}
	if !info.Members.Members[0].IsFromMe {
		t.Fatalf("first member = %+v, want self member", info.Members.Members[0])
	}
	if info.Members.Members[1].Sender != networkid.UserID(syntheticOtherUserID) {
		t.Fatalf("second member sender = %q", info.Members.Members[1].Sender)
	}
}
