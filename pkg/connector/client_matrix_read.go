package connector

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

// HandleMatrixReadReceipt forwards Matrix read receipts to TikTok's mark_read API
// so the conversation clears on the remote side when the user reads in Matrix.
func (tc *TikTokClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	if msg == nil || msg.Portal == nil {
		return nil
	}
	if msg.Implicit {
		return nil
	}

	log := zerolog.Ctx(ctx).With().Str("component", "connector-matrix-read").Logger()
	ctx = log.WithContext(ctx)

	conv, err := tc.getConversationForPortal(ctx, msg.Portal)
	if err != nil {
		return fmt.Errorf("resolve TikTok conversation for mark_read: %w", err)
	}

	// read_message_index must match ConversationMessageEntry.timestamp_us (proto
	// field 4) for the latest message — not server_message_id (3) or cursor_ts_us (25).
	readIndex, err := tc.apiClient.LatestMessageTimestampUs(ctx, conv)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to fetch latest message timestamp_us for mark_read; trying local fallbacks")
	}
	if readIndex == 0 && msg.ExactMessage != nil {
		if us := msg.ExactMessage.Timestamp.UnixMicro(); us > 0 {
			readIndex = uint64(us)
		}
	}
	if readIndex == 0 {
		if meta, ok := portalMeta(msg.Portal); ok && meta.LastSeenMessageTimestampMs > 0 {
			readIndex = uint64(meta.LastSeenMessageTimestampMs) * 1000
		}
	}
	if readIndex == 0 {
		log.Debug().
			Str("portal_id", string(msg.Portal.ID)).
			Str("event_id", string(msg.EventID)).
			Msg("Skipping TikTok mark_read: no read_message_index (timestamp_us)")
		return nil
	}

	if err := tc.apiClient.MarkConversationRead(ctx, libtiktok.MarkConversationReadParams{
		ConvID:           conv.ID,
		ConvSourceID:     conv.SourceID,
		ConversationType: conv.ConversationType,
		ReadMessageIndex: readIndex,
		TotalUnreadCount: 0,
		ConvUnreadCount:  0,
	}); err != nil {
		return fmt.Errorf("mark TikTok conversation read: %w", err)
	}

	log.Debug().
		Str("conversation_id", conv.ID).
		Uint64("read_message_index", readIndex).
		Msg("Marked TikTok conversation read")
	return nil
}
