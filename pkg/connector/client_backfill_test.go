package connector

import (
	"testing"
	"time"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestConnectorInitialBackfillDefaultsAndClamp(t *testing.T) {
	tc := &TikTokConnector{}
	if got := tc.initialBackfillMaxPages(); got != defaultInitialBackfillMaxPages {
		t.Fatalf("initialBackfillMaxPages() = %d, want %d", got, defaultInitialBackfillMaxPages)
	}
	if got := tc.initialBackfillMaxConversations(); got != 0 {
		t.Fatalf("initialBackfillMaxConversations() = %d, want 0", got)
	}
	if got := tc.initialBackfillLookback(); got != 72*time.Hour {
		t.Fatalf("initialBackfillLookback() = %s, want 72h", got)
	}

	tc.Config.InitialBackfillMaxPages = hardMaxBackfillPagesPerConversation + 1
	if got := tc.initialBackfillMaxPages(); got != hardMaxBackfillPagesPerConversation {
		t.Fatalf("initialBackfillMaxPages() with clamp = %d, want %d", got, hardMaxBackfillPagesPerConversation)
	}
}

func TestCheckpointFromPortal(t *testing.T) {
	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey: networkid.PortalKey{ID: networkid.PortalID("conv")},
			Metadata: &PortalMetadata{
				LastSeenMessageTimestampMs: 1234,
				LastSeenMessageID:          55,
				LastSeenCursorTsUs:         987654321,
			},
		},
	}

	got := checkpointFromPortal(portal)
	if got.TimestampMs != 1234 || got.MessageID != 55 || got.CursorTsUs != 987654321 {
		t.Fatalf("checkpointFromPortal() = %+v", got)
	}
}

func TestMessageIsAfterCheckpoint(t *testing.T) {
	checkpoint := backfillCheckpoint{TimestampMs: 2000, MessageID: 20}

	tests := []struct {
		name string
		msg  libtiktok.Message
		want bool
	}{
		{
			name: "newer timestamp",
			msg:  libtiktok.Message{ServerID: 30, TimestampMs: 3000},
			want: true,
		},
		{
			name: "same message",
			msg:  libtiktok.Message{ServerID: 20, TimestampMs: 2000},
			want: false,
		},
		{
			name: "same timestamp different id stays eligible",
			msg:  libtiktok.Message{ServerID: 21, TimestampMs: 2000},
			want: true,
		},
		{
			name: "older timestamp",
			msg:  libtiktok.Message{ServerID: 19, TimestampMs: 1999},
			want: false,
		},
	}

	for _, tt := range tests {
		if got := messageIsAfterCheckpoint(tt.msg, checkpoint); got != tt.want {
			t.Fatalf("%s: messageIsAfterCheckpoint() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestPageReachedCheckpoint(t *testing.T) {
	checkpoint := backfillCheckpoint{TimestampMs: 2000, MessageID: 20}

	if !pageReachedCheckpoint([]libtiktok.Message{
		{ServerID: 25, TimestampMs: 2500},
		{ServerID: 20, TimestampMs: 2000},
	}, checkpoint) {
		t.Fatal("pageReachedCheckpoint() = false, want true when exact checkpoint is present")
	}

	if !pageReachedCheckpoint([]libtiktok.Message{
		{ServerID: 25, TimestampMs: 2500},
		{ServerID: 19, TimestampMs: 1900},
	}, checkpoint) {
		t.Fatal("pageReachedCheckpoint() = false, want true when older history is reached")
	}

	if pageReachedCheckpoint([]libtiktok.Message{
		{ServerID: 25, TimestampMs: 2500},
		{ServerID: 21, TimestampMs: 2100},
	}, checkpoint) {
		t.Fatal("pageReachedCheckpoint() = true, want false before checkpoint boundary")
	}
}

func TestPageReachedColdStartCutoff(t *testing.T) {
	cutoffMs := int64(2000)

	if !pageReachedColdStartCutoff([]libtiktok.Message{
		{ServerID: 25, TimestampMs: 2500},
		{ServerID: 19, TimestampMs: 1999},
	}, cutoffMs) {
		t.Fatal("pageReachedColdStartCutoff() = false, want true when page crosses cutoff")
	}

	if pageReachedColdStartCutoff([]libtiktok.Message{
		{ServerID: 25, TimestampMs: 2500},
		{ServerID: 20, TimestampMs: 2000},
	}, cutoffMs) {
		t.Fatal("pageReachedColdStartCutoff() = true, want false when all messages are within lookback")
	}
}

func TestUpdateCheckpointFromMessage(t *testing.T) {
	checkpoint := backfillCheckpoint{}

	if !updateCheckpointFromMessage(&checkpoint, libtiktok.Message{
		ServerID:    44,
		TimestampMs: 5000,
		CursorTsUs:  777,
	}) {
		t.Fatal("updateCheckpointFromMessage() = false, want true for first message")
	}
	if checkpoint.TimestampMs != 5000 || checkpoint.MessageID != 44 || checkpoint.CursorTsUs != 777 {
		t.Fatalf("checkpoint after first update = %+v", checkpoint)
	}

	if updateCheckpointFromMessage(&checkpoint, libtiktok.Message{
		ServerID:    43,
		TimestampMs: 4999,
		CursorTsUs:  666,
	}) {
		t.Fatal("updateCheckpointFromMessage() = true, want false for older message")
	}

	if !persistCheckpoint(&PortalMetadata{}, checkpoint) {
		t.Fatal("persistCheckpoint() = false, want true when writing checkpoint fields")
	}
}
