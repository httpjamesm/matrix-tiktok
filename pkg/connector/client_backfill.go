package connector

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

const (
	// hardMaxBackfillPagesPerConversation is a safety clamp for user-configured
	// startup backfill limits so a stuck cursor cannot loop forever.
	hardMaxBackfillPagesPerConversation = 10000
	// defaultInitialBackfillMaxPages caps incremental catch-up pagination on
	// connect when the user leaves initial_backfill_max_pages at zero and a
	// portal already has a stored checkpoint.
	defaultInitialBackfillMaxPages = 5
)

type backfillCheckpoint struct {
	TimestampMs int64
	MessageID   uint64
	CursorTsUs  uint64
}

func (tc *TikTokConnector) initialBackfillMaxPages(coldStart bool) int {
	pages := tc.Config.InitialBackfillMaxPages
	switch {
	case pages <= 0:
		if coldStart {
			return hardMaxBackfillPagesPerConversation
		}
		return defaultInitialBackfillMaxPages
	case pages > hardMaxBackfillPagesPerConversation:
		return hardMaxBackfillPagesPerConversation
	default:
		return pages
	}
}

func (tc *TikTokConnector) initialBackfillMaxConversations() int {
	if tc == nil || tc.Config.InitialBackfillMaxConversations <= 0 {
		return 0
	}
	return tc.Config.InitialBackfillMaxConversations
}

// initialBackfillLookback returns a maximum age for cold-start history when
// InitialBackfillLookbackHours is positive. Zero means no time cutoff (fetch
// until the API runs out of history, subject to the page cap).
func (tc *TikTokConnector) initialBackfillLookback() time.Duration {
	if tc == nil || tc.Config.InitialBackfillLookbackHours <= 0 {
		return 0
	}
	return time.Duration(tc.Config.InitialBackfillLookbackHours) * time.Hour
}

func maxCheckpoint(a, b backfillCheckpoint) backfillCheckpoint {
	switch {
	case a.TimestampMs > b.TimestampMs:
		return a
	case b.TimestampMs > a.TimestampMs:
		return b
	case a.TimestampMs == 0:
		return a
	case b.MessageID != 0:
		return b
	default:
		return a
	}
}

func checkpointFromPortal(portal *bridgev2.Portal) backfillCheckpoint {
	meta, ok := portalMeta(portal)
	if !ok {
		return backfillCheckpoint{}
	}
	return backfillCheckpoint{
		TimestampMs: meta.LastSeenMessageTimestampMs,
		MessageID:   meta.LastSeenMessageID,
		CursorTsUs:  meta.LastSeenCursorTsUs,
	}
}

func checkpointFromRuntime(lastSeen int64) backfillCheckpoint {
	if lastSeen <= 0 {
		return backfillCheckpoint{}
	}
	return backfillCheckpoint{TimestampMs: lastSeen}
}

func checkpointAdvanced(next, current backfillCheckpoint) bool {
	if next.TimestampMs != current.TimestampMs {
		return next.TimestampMs > current.TimestampMs
	}
	if next.TimestampMs == 0 {
		return false
	}
	if next.MessageID != current.MessageID {
		return next.MessageID != 0
	}
	return next.CursorTsUs != 0 && next.CursorTsUs != current.CursorTsUs
}

func updateCheckpointFromMessage(current *backfillCheckpoint, msg libtiktok.Message) bool {
	next := backfillCheckpoint{
		TimestampMs: msg.TimestampMs,
		MessageID:   msg.ServerID,
		CursorTsUs:  msg.CursorTsUs,
	}
	if !checkpointAdvanced(next, *current) {
		return false
	}
	*current = next
	return true
}

func messageIsAfterCheckpoint(msg libtiktok.Message, checkpoint backfillCheckpoint) bool {
	if checkpoint.TimestampMs == 0 {
		return true
	}
	if msg.TimestampMs > checkpoint.TimestampMs {
		return true
	}
	if msg.TimestampMs < checkpoint.TimestampMs {
		return false
	}
	return checkpoint.MessageID == 0 || msg.ServerID != checkpoint.MessageID
}

func pageReachedCheckpoint(msgs []libtiktok.Message, checkpoint backfillCheckpoint) bool {
	if checkpoint.TimestampMs == 0 {
		return false
	}
	for _, msg := range msgs {
		if msg.TimestampMs < checkpoint.TimestampMs {
			return true
		}
		if msg.TimestampMs == checkpoint.TimestampMs && checkpoint.MessageID != 0 && msg.ServerID == checkpoint.MessageID {
			return true
		}
	}
	return false
}

func pageReachedColdStartCutoff(msgs []libtiktok.Message, cutoffMs int64) bool {
	if cutoffMs <= 0 {
		return false
	}
	for _, msg := range msgs {
		if msg.TimestampMs < cutoffMs {
			return true
		}
	}
	return false
}

// historyPageFingerprint hashes a non-empty page of messages so we can detect
// when TikTok returns the same window again while claiming a new cursor.
func historyPageFingerprint(msgs []libtiktok.Message) uint64 {
	if len(msgs) == 0 {
		return 0
	}
	var h uint64 = uint64(len(msgs))
	for _, m := range msgs {
		h = h*1315423911 ^ m.ServerID
	}
	return h
}

func persistCheckpoint(meta *PortalMetadata, checkpoint backfillCheckpoint) bool {
	if meta == nil {
		return false
	}
	changed := false
	if meta.LastSeenMessageTimestampMs != checkpoint.TimestampMs {
		meta.LastSeenMessageTimestampMs = checkpoint.TimestampMs
		changed = true
	}
	if meta.LastSeenMessageID != checkpoint.MessageID {
		meta.LastSeenMessageID = checkpoint.MessageID
		changed = true
	}
	if meta.LastSeenCursorTsUs != checkpoint.CursorTsUs {
		meta.LastSeenCursorTsUs = checkpoint.CursorTsUs
		changed = true
	}
	return changed
}

func portalMeta(portal *bridgev2.Portal) (*PortalMetadata, bool) {
	if portal == nil {
		return nil, false
	}
	meta, ok := portal.Metadata.(*PortalMetadata)
	return meta, ok && meta != nil
}

func ensurePortalMeta(log zerolog.Logger, portal *bridgev2.Portal) *PortalMetadata {
	if portal == nil {
		return nil
	}
	if meta, ok := portalMeta(portal); ok {
		return meta
	}
	if portal.Metadata != nil {
		log.Error().
			Str("portal_id", string(portal.ID)).
			Str("metadata_type", fmt.Sprintf("%T", portal.Metadata)).
			Msg("Unexpected portal metadata type; resetting TikTok portal metadata")
	}
	meta := &PortalMetadata{}
	portal.Metadata = meta
	return meta
}

func mergePortalMetadata(meta *PortalMetadata, conv *libtiktok.Conversation) bool {
	if meta == nil || conv == nil {
		return false
	}
	changed := false
	if meta.ConversationID != conv.ID {
		meta.ConversationID = conv.ID
		changed = true
	}
	if meta.SourceID != conv.SourceID {
		meta.SourceID = conv.SourceID
		changed = true
	}
	if meta.GroupName != conv.Name {
		meta.GroupName = conv.Name
		changed = true
	}
	if meta.ConversationType != conv.ConversationType {
		meta.ConversationType = conv.ConversationType
		changed = true
	}
	if conv.Muted != nil && !equalBoolPtr(meta.Muted, conv.Muted) {
		meta.Muted = conv.Muted
		changed = true
	}
	return changed
}

func mergePortalGroupName(meta *PortalMetadata, groupName string) bool {
	if meta == nil || meta.GroupName == groupName {
		return false
	}
	meta.GroupName = groupName
	return true
}

func (tc *TikTokClient) buildGroupChatInfo(conv *libtiktok.Conversation) *bridgev2.ChatInfo {
	if conv == nil || conv.ConversationType != 2 || conv.Name == "" {
		return nil
	}
	name := conv.Name
	return &bridgev2.ChatInfo{
		Name:      &name,
		UserLocal: userLocalPortalInfoFromMuted(conv.Muted),
		ExtraUpdates: func(ctx context.Context, portal *bridgev2.Portal) bool {
			return mergePortalMetadata(ensurePortalMeta(*zerolog.Ctx(ctx), portal), conv)
		},
	}
}

func (tc *TikTokClient) queueGroupNameUpdate(conv *libtiktok.Conversation) {
	info := tc.buildGroupChatInfo(conv)
	if info == nil {
		return
	}
	tc.userLogin.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventChatInfoChange,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.
					Str("conversation_id", conv.ID).
					Str("group_name", conv.Name)
			},
			PortalKey: networkid.PortalKey{
				ID:       makePortalID(conv.ID),
				Receiver: tc.userLogin.ID,
			},
			Sender: bridgev2.EventSender{
				IsFromMe: true,
				Sender:   makeUserID(tc.meta.UserID),
			},
			Timestamp: time.Now(),
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: info,
		},
	})
}

// fetchAndDispatch is called once on Connect to backfill message history via
// the REST API before the WebSocket takes over. It walks each inbox
// conversation and queues any unseen messages into the bridgev2 event pipeline,
// also populating the otherUsers cache so GetChatInfo works immediately.
//
// Deduplication relies on two layers:
//  1. The in-memory lastSeen map suppresses re-dispatches within a process lifetime.
//  2. The bridge database deduplicates by message ID across restarts.
func (tc *TikTokClient) fetchAndDispatch(ctx context.Context) error {
	log := zerolog.Ctx(ctx).With().Str("component", "connector-backfill").Logger()
	ctx = log.WithContext(ctx)

	convs, err := tc.apiClient.GetInbox(ctx)
	if err != nil {
		return fmt.Errorf("get inbox: %w", err)
	}

	log.Debug().Int("conversations", len(convs)).Msg("Inbox fetched")
	if len(convs) == 0 {
		log.Debug().Msg("Inbox is empty — no conversations to process")
		return nil
	}

	maxConversations := tc.connector.initialBackfillMaxConversations()
	if maxConversations > 0 && len(convs) > maxConversations {
		log.Info().
			Int("requested_conversations", len(convs)).
			Int("limited_conversations", maxConversations).
			Msg("Limiting startup backfill to the newest inbox conversations")
		convs = convs[:maxConversations]
	}

	lookback := tc.connector.initialBackfillLookback()

	for i := range convs {
		conv := &convs[i]
		log.Debug().
			Str("conversation_id", conv.ID).
			Strs("participants", conv.Participants).
			Str("name", conv.Name).
			Uint64("source_id", conv.SourceID).
			Msg("Processing conversation")

		if conv.Name != "" {
			tc.mu.Lock()
			tc.groupNames[conv.ID] = conv.Name
			tc.mu.Unlock()
			log.Debug().
				Str("conversation_id", conv.ID).
				Str("group_name", conv.Name).
				Msg("Updated group-name cache from inbox conversation")
			tc.queueGroupNameUpdate(conv)
		}

		for _, pid := range conv.Participants {
			if pid != tc.meta.UserID {
				tc.mu.Lock()
				tc.otherUsers[conv.ID] = pid
				tc.mu.Unlock()
				log.Debug().
					Str("conversation_id", conv.ID).
					Str("other_user_id", pid).
					Str("self_user_id", tc.meta.UserID).
					Msg("Updated other-user cache from inbox conversation")
				break
			}
		}
		portal, err := tc.syncPortalMetadata(ctx, conv)
		if err != nil {
			log.Err(err).Str("conversation_id", conv.ID).Msg("Failed to persist portal metadata for conversation")
			continue
		}

		tc.mu.Lock()
		lastSeen := tc.lastSeen[conv.ID]
		tc.mu.Unlock()
		storedCheckpoint := checkpointFromPortal(portal)
		checkpoint := maxCheckpoint(storedCheckpoint, checkpointFromRuntime(lastSeen))
		coldStart := checkpoint.TimestampMs == 0
		maxPages := tc.connector.initialBackfillMaxPages(coldStart)
		coldStartCutoffMs := int64(0)
		if coldStart && lookback > 0 {
			coldStartCutoffMs = time.Now().Add(-lookback).UnixMilli()
		}

		// Paginate get_by_conversation newest-first, but stop once we reach a
		// stored checkpoint, optional cold-start lookback (if configured), or the
		// page cap. We still dispatch oldest-first overall by walking the
		// collected pages in reverse.
		var pages [][]libtiktok.Message
		cursor := ""
		truncated := false
		stopReason := ""
		var prevPageFingerprint uint64
		for page := 0; ; page++ {
			if page >= maxPages {
				truncated = true
				break
			}
			msgs, next, err := tc.apiClient.GetMessages(ctx, conv, cursor)
			if err != nil {
				log.Err(err).
					Str("conversation_id", conv.ID).
					Int("backfill_page", page).
					Msg("Error fetching messages for conversation")
				break
			}
			fp := historyPageFingerprint(msgs)
			if fp != 0 && page > 0 && fp == prevPageFingerprint {
				log.Warn().
					Str("conversation_id", conv.ID).
					Int("backfill_page", page).
					Msg("TikTok history returned an identical message page; stopping to avoid a pagination loop")
				stopReason = "repeated page"
				break
			}
			prevPageFingerprint = fp

			pages = append(pages, msgs)
			if pageReachedCheckpoint(msgs, checkpoint) {
				stopReason = "checkpoint reached"
				break
			}
			if checkpoint.TimestampMs == 0 && pageReachedColdStartCutoff(msgs, coldStartCutoffMs) {
				stopReason = "cold-start cutoff reached"
				break
			}
			if next == "" {
				stopReason = "history exhausted"
				break
			}
			if next == cursor {
				log.Warn().
					Str("conversation_id", conv.ID).
					Str("cursor", next).
					Msg("TikTok history cursor did not advance; stopping backfill pagination")
				stopReason = "cursor did not advance"
				break
			}
			cursor = next
		}
		if truncated {
			log.Warn().
				Str("conversation_id", conv.ID).
				Int("max_pages", maxPages).
				Msg("Backfill pagination stopped at safety limit (history may be truncated)")
		}

		totalMsgs := 0
		for i := range pages {
			totalMsgs += len(pages[i])
		}
		log.Debug().
			Str("conversation_id", conv.ID).
			Int("messages", totalMsgs).
			Int("pages", len(pages)).
			Int64("last_seen_ms", lastSeen).
			Str("stop_reason", stopReason).
			Int64("stored_checkpoint_ms", storedCheckpoint.TimestampMs).
			Uint64("stored_checkpoint_id", storedCheckpoint.MessageID).
			Int64("cold_start_cutoff_ms", coldStartCutoffMs).
			Msg("Fetched messages for conversation")

		var dispatched int
		updatedCheckpoint := checkpoint
		for pi := len(pages) - 1; pi >= 0; pi-- {
			for _, msg := range pages[pi] {
				msgLog := log.Debug().
					Str("conversation_id", conv.ID).
					Uint64("message_id", msg.ServerID).
					Str("sender_id", msg.SenderID).
					Str("type", msg.Type).
					Int64("ts_ms", msg.TimestampMs).
					Int64("last_seen_ms", lastSeen).
					Int64("checkpoint_ms", checkpoint.TimestampMs)

				if !messageIsAfterCheckpoint(msg, checkpoint) {
					msgLog.Bool("skipped", true).Msg("Skipping already-seen message during backfill")
					continue
				}
				if coldStartCutoffMs > 0 && msg.TimestampMs < coldStartCutoffMs {
					msgLog.Bool("skipped", true).Msg("Skipping cold-start message older than lookback window")
					continue
				}
				msgLog.Bool("skipped", false).Msg("Dispatching backfill message")
				tc.dispatchMessage(conv, msg)
				dispatched++
				updateCheckpointFromMessage(&updatedCheckpoint, msg)
				lastSeen = updatedCheckpoint.TimestampMs
			}
		}

		log.Debug().
			Str("conversation_id", conv.ID).
			Int("dispatched", dispatched).
			Msg("Finished processing conversation")

		tc.mu.Lock()
		if lastSeen > tc.lastSeen[conv.ID] {
			tc.lastSeen[conv.ID] = lastSeen
			log.Debug().
				Str("conversation_id", conv.ID).
				Int64("last_seen_ms", lastSeen).
				Msg("Updated last-seen cache after backfill")
		}
		tc.mu.Unlock()
		if checkpointAdvanced(updatedCheckpoint, storedCheckpoint) {
			meta := ensurePortalMeta(log, portal)
			if persistCheckpoint(meta, updatedCheckpoint) {
				log.Debug().
					Str("conversation_id", conv.ID).
					Int64("last_seen_message_timestamp_ms", updatedCheckpoint.TimestampMs).
					Uint64("last_seen_message_id", updatedCheckpoint.MessageID).
					Uint64("last_seen_cursor_ts_us", updatedCheckpoint.CursorTsUs).
					Msg("Persisting backfill checkpoint to portal metadata")
				if err := portal.Save(ctx); err != nil {
					log.Err(err).Str("conversation_id", conv.ID).Msg("Failed to save portal backfill checkpoint")
				}
			}
		}
	}
	return nil
}

// IsThisUser reports whether the given remote user ID belongs to this login.
func (tc *TikTokClient) IsThisUser(_ context.Context, userID networkid.UserID) bool {
	return makeUserID(tc.meta.UserID) == userID
}

// GetCapabilities returns the Matrix room feature-set for this login.
func (tc *TikTokClient) GetCapabilities(_ context.Context, _ *bridgev2.Portal) *event.RoomFeatures {
	imageUpload := &event.FileFeatures{
		MimeTypes: map[string]event.CapabilitySupportLevel{
			"image/*": event.CapLevelFullySupported,
		},
		Caption: event.CapLevelRejected,
		MaxSize: tiktokMatrixMediaMaxBytes,
	}
	videoUpload := &event.FileFeatures{
		MimeTypes: map[string]event.CapabilitySupportLevel{
			"video/*":         event.CapLevelFullySupported,
			"video/mp4":       event.CapLevelFullySupported,
			"video/quicktime": event.CapLevelFullySupported,
		},
		Caption: event.CapLevelRejected,
		MaxSize: tiktokMatrixMediaMaxBytes,
	}
	matrixFileMedia := &event.FileFeatures{
		MimeTypes: map[string]event.CapabilitySupportLevel{
			"image/*":          event.CapLevelFullySupported,
			"video/*":          event.CapLevelFullySupported,
			"video/mp4":        event.CapLevelFullySupported,
			"video/webm":       event.CapLevelFullySupported,
			"video/quicktime":  event.CapLevelFullySupported,
			"video/x-matroska": event.CapLevelFullySupported,
		},
		Caption: event.CapLevelRejected,
		MaxSize: tiktokMatrixMediaMaxBytes,
	}
	return &event.RoomFeatures{
		MaxTextLength: 1000,
		Delete:        event.CapLevelFullySupported,
		Reply:         event.CapLevelFullySupported,
		File: event.FileFeatureMap{
			event.MsgImage: imageUpload,
			event.MsgVideo: videoUpload,
			event.MsgFile:  matrixFileMedia,
		},
	}
}

// GetChatInfo returns Matrix room metadata for a bridged TikTok conversation.
// For DMs, the other participant is read from the in-memory otherUsers cache
// populated during fetchAndDispatch. If the conversation hasn't been seen yet
// (e.g. a portal is being reconstructed from the database on startup), the
// portal ID is used as a placeholder and the room will refresh on the next poll.
func (tc *TikTokClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	meta, _ := portalMeta(portal)
	if meta == nil || meta.ConversationType == 0 {
		if _, err := tc.getConversationForPortal(ctx, portal); err != nil && (meta == nil || meta.ConversationType == 0) {
			return nil, fmt.Errorf("load TikTok chat info for %s: %w", portal.ID, err)
		}
		meta, _ = portalMeta(portal)
	}

	tc.mu.Lock()
	groupName := tc.groupNames[string(portal.ID)]
	tc.mu.Unlock()

	otherUserID := string(portal.OtherUserID)
	if otherUserID == "" {
		tc.mu.Lock()
		otherUserID = tc.otherUsers[string(portal.ID)]
		tc.mu.Unlock()
	}
	if groupName == "" && meta != nil {
		groupName = meta.GroupName
	}

	isGroup := meta != nil && meta.ConversationType == 2
	isDM := meta != nil && meta.ConversationType == 1 && otherUserID != ""

	members := []bridgev2.ChatMember{{
		EventSender: bridgev2.EventSender{
			IsFromMe: true,
			Sender:   makeUserID(tc.meta.UserID),
		},
		Membership: event.MembershipJoin,
		PowerLevel: ptrInt(50),
	}}
	if otherUserID != "" {
		members = append(members, bridgev2.ChatMember{
			EventSender: bridgev2.EventSender{
				Sender: makeUserID(otherUserID),
			},
			Membership: event.MembershipJoin,
			PowerLevel: ptrInt(50),
		})
	}

	info := &bridgev2.ChatInfo{
		Members: &bridgev2.ChatMemberList{
			IsFull:  true,
			Members: members,
		},
	}
	if meta != nil {
		info.UserLocal = userLocalPortalInfoFromMuted(meta.Muted)
	}
	if isDM {
		roomType := database.RoomTypeDM
		info.Type = &roomType
		info.Members.OtherUserID = makeUserID(otherUserID)
	}
	if isGroup && groupName != "" {
		info.Name = &groupName
		info.ExtraUpdates = func(ctx context.Context, portal *bridgev2.Portal) bool {
			return mergePortalGroupName(ensurePortalMeta(*zerolog.Ctx(ctx), portal), groupName)
		}
	}
	return info, nil
}

func (tc *TikTokClient) updatePortalMetadata(portal *bridgev2.Portal, conv *libtiktok.Conversation) bool {
	if portal == nil || conv == nil {
		return false
	}
	log := zerolog.Nop()
	if tc.userLogin != nil {
		log = tc.userLogin.Log
	}
	meta := ensurePortalMeta(log, portal)
	changed := mergePortalMetadata(meta, conv)

	otherUserID := ""
	for _, pid := range conv.Participants {
		if pid != "" && pid != tc.meta.UserID {
			otherUserID = pid
			break
		}
	}

	wantRoomType := database.RoomTypeDefault
	if conv.ConversationType == 1 {
		wantRoomType = database.RoomTypeDM
	}
	if portal.RoomType != wantRoomType {
		portal.RoomType = wantRoomType
		changed = true
	}

	wantOtherUserID := networkid.UserID("")
	if conv.ConversationType == 1 {
		wantOtherUserID = makeUserID(otherUserID)
	}
	if portal.OtherUserID != wantOtherUserID {
		portal.OtherUserID = wantOtherUserID
		changed = true
	}
	if changed {
		log.Debug().
			Str("portal_id", string(portal.ID)).
			Str("conversation_id", conv.ID).
			Uint64("source_id", conv.SourceID).
			Uint64("conversation_type", conv.ConversationType).
			Msg("Portal metadata changed from TikTok conversation state")
	}
	return changed
}

func (tc *TikTokClient) syncPortalMetadata(ctx context.Context, conv *libtiktok.Conversation) (*bridgev2.Portal, error) {
	log := zerolog.Ctx(ctx)
	portal, err := tc.userLogin.Bridge.GetPortalByKey(ctx, networkid.PortalKey{
		ID:       makePortalID(conv.ID),
		Receiver: tc.userLogin.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("get portal for metadata sync: %w", err)
	}
	if tc.updatePortalMetadata(portal, conv) {
		log.Debug().
			Str("portal_id", string(portal.ID)).
			Str("conversation_id", conv.ID).
			Msg("Saving updated portal metadata")
		if err := portal.Save(ctx); err != nil {
			return nil, fmt.Errorf("save portal metadata: %w", err)
		}
	} else {
		log.Debug().
			Str("portal_id", string(portal.ID)).
			Str("conversation_id", conv.ID).
			Msg("Portal metadata already up to date")
	}
	tc.applyPortalMuteState(ctx, portal, conv.Muted)
	return portal, nil
}

func (tc *TikTokClient) getConversationForPortal(ctx context.Context, portal *bridgev2.Portal) (*libtiktok.Conversation, error) {
	log := zerolog.Ctx(ctx)
	convID := string(portal.ID)
	meta, metaOK := portalMeta(portal)
	if metaOK {
		if meta.ConversationID != "" {
			convID = meta.ConversationID
		}
		// Require conversation_type before skipping the inbox fetch. Older bridge
		// versions cached source_id only; those portals stayed at type 0 in RAM
		// even after the DB row gained conversation_type:2, so group flags
		// (reactions, send envelope) were wrong until the next full metadata refresh.
		if meta.SourceID != 0 && meta.ConversationType != 0 {
			log.Debug().
				Str("portal_id", string(portal.ID)).
				Str("conversation_id", convID).
				Msg("Resolved conversation from cached portal metadata")
			return &libtiktok.Conversation{
				ID:               convID,
				SourceID:         meta.SourceID,
				Name:             meta.GroupName,
				ConversationType: meta.ConversationType,
				Muted:            meta.Muted,
			}, nil
		}
	}

	convs, err := tc.apiClient.GetInbox(ctx)
	if err != nil {
		return nil, fmt.Errorf("get TikTok inbox for conversation lookup: %w", err)
	}
	for i := range convs {
		if convs[i].ID != convID {
			continue
		}

		if tc.updatePortalMetadata(portal, &convs[i]) {
			log.Debug().
				Str("portal_id", string(portal.ID)).
				Str("conversation_id", convs[i].ID).
				Msg("Caching portal metadata discovered via inbox lookup")
			if err := portal.Save(ctx); err != nil {
				return nil, fmt.Errorf("cache TikTok portal metadata: %w", err)
			}
		}
		log.Debug().
			Str("portal_id", string(portal.ID)).
			Str("conversation_id", convs[i].ID).
			Msg("Resolved conversation by scanning inbox")
		return &convs[i], nil
	}

	return nil, fmt.Errorf("TikTok conversation %q not found in inbox", convID)
}
