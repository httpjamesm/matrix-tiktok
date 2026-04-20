package connector

import (
	"context"
	"testing"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestGetChatInfo_UsesStoredGroupName(t *testing.T) {
	tc := &TikTokClient{
		meta:       &UserLoginMetadata{UserID: "1111111111111111111"},
		otherUsers: map[string]string{"7587998693467750664": "2222222222222222222"},
		groupNames: map[string]string{},
	}
	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey: networkid.PortalKey{
				ID: makePortalID("7587998693467750664"),
			},
			Metadata: &PortalMetadata{
				ConversationID:   "7587998693467750664",
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
	if info.Members == nil || len(info.Members.Members) != 2 {
		t.Fatalf("info.Members = %+v, want 2 members", info.Members)
	}
}

func TestBuildGroupChatInfo_PersistsPortalMetadata(t *testing.T) {
	tc := &TikTokClient{}
	conv := &libtiktok.Conversation{
		ID:               "7587998693467750664",
		SourceID:         7587998693467750664,
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
