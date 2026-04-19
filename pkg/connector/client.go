package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

// tiktokMatrixMediaMaxBytes is advertised in com.beeper.room_features for media uploads
// and is a soft cap for clients; TikTok may still reject oversized payloads.
const tiktokMatrixMediaMaxBytes = 50 * 1024 * 1024

// TikTokClient implements bridgev2.NetworkAPI for a single logged-in TikTok session.
type TikTokClient struct {
	connector *TikTokConnector
	userLogin *bridgev2.UserLogin
	meta      *UserLoginMetadata
	apiClient *libtiktok.Client

	stopLoop    context.CancelFunc
	isConnected bool

	// In-memory state — reset on restart, but the bridge deduplicates by message ID.
	mu         sync.Mutex
	lastSeen   map[string]int64  // convID → highest dispatched message timestamp (ms)
	otherUsers map[string]string // convID → other participant's TikTok user ID
}

// newTikTokClient is the canonical constructor used by both LoadUserLogin and
// TikTokLogin.finishLogin.
func newTikTokClient(connector *TikTokConnector, userLogin *bridgev2.UserLogin, meta *UserLoginMetadata) *TikTokClient {
	return &TikTokClient{
		connector:  connector,
		userLogin:  userLogin,
		meta:       meta,
		apiClient:  libtiktok.NewClient(meta.Cookies),
		lastSeen:   make(map[string]int64),
		otherUsers: make(map[string]string),
	}
}

// Ensure TikTokClient fully implements the required interfaces.
var _ bridgev2.NetworkAPI = (*TikTokClient)(nil)
var _ bridgev2.IdentifierResolvingNetworkAPI = (*TikTokClient)(nil)
var _ bridgev2.ReactionHandlingNetworkAPI = (*TikTokClient)(nil)
var _ bridgev2.RedactionHandlingNetworkAPI = (*TikTokClient)(nil)

// ────────────────────────────────────────────────────────────────────────────
// Connection lifecycle
// ────────────────────────────────────────────────────────────────────────────

// Connect validates the TikTok session, performs a one-time REST backfill to
// catch up on recent messages, then starts the WebSocket loop for real-time
// delivery.  Errors are reported via BridgeState rather than returned
// (mautrix-go March 2025 convention).
func (tc *TikTokClient) Connect(ctx context.Context) {
	log := zerolog.Ctx(ctx)

	if _, err := tc.apiClient.GetSelf(ctx); err != nil {
		tc.userLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      "tiktok-auth-error",
			Message:    "TikTok session is no longer valid — please log in again",
			Info:       map[string]any{"go_error": err.Error()},
		})
		return
	}

	log.Info().
		Str("user_id", tc.meta.UserID).
		Str("username", tc.meta.Username).
		Msg("TikTok session validated, performing initial backfill")

	// One-time history backfill via REST: catches up on recent messages and
	// populates the otherUsers cache so GetChatInfo works immediately.
	// The WebSocket only delivers events that arrive after it connects, so
	// this ensures nothing is missed during startup.
	if err := tc.fetchAndDispatch(ctx); err != nil {
		log.Warn().Err(err).Msg("Initial REST backfill failed, proceeding to WebSocket anyway")
	}

	tc.isConnected = true
	tc.userLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateConnected,
	})

	// Fresh background context so the loop outlives the Connect call's context.
	loopCtx, cancel := context.WithCancel(context.Background())
	tc.stopLoop = cancel
	go tc.wsLoop(loopCtx)
}

// Disconnect stops the WebSocket loop.
func (tc *TikTokClient) Disconnect() {
	if tc.stopLoop != nil {
		tc.stopLoop()
		tc.stopLoop = nil
	}
	tc.isConnected = false
}

// IsLoggedIn returns the cached connection state. Must not do IO.
func (tc *TikTokClient) IsLoggedIn() bool {
	return tc.isConnected
}

// LogoutRemote is a no-op — the unofficial TikTok API has no logout endpoint.
func (tc *TikTokClient) LogoutRemote(_ context.Context) {}

// ────────────────────────────────────────────────────────────────────────────
// WebSocket loop
// ────────────────────────────────────────────────────────────────────────────

// wsLoop dials the TikTok IM WebSocket and dispatches incoming chat events
// until ctx is cancelled.  On disconnect it waits with exponential back-off
// before reconnecting so that transient server errors do not cause a tight
// reconnect storm.
func (tc *TikTokClient) wsLoop(ctx context.Context) {
	log := tc.userLogin.Log.With().Str("component", "tiktok-ws").Logger()
	ctx = log.WithContext(ctx)

	log.Info().Msg("Starting TikTok IM WebSocket loop")
	defer log.Info().Msg("TikTok IM WebSocket loop stopped")

	backoff := 5 * time.Second
	const maxBackoff = 5 * time.Minute

	for {
		ch, err := tc.apiClient.ConnectWebSocket(ctx)
		if err != nil {
			log.Err(err).Dur("retry_in", backoff).Msg("WebSocket dial failed, will retry")
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
		}
		backoff = 5 * time.Second // reset after a successful dial

		log.Info().Msg("WebSocket connected, listening for messages")

		for evt := range ch {
			switch {
			case evt.Message != nil:
				wsMsg := evt.Message
				log.Debug().
					Str("conv_id", wsMsg.Conversation.ID).
					Str("sender_id", wsMsg.Message.SenderID).
					Str("type", wsMsg.Message.Type).
					Uint64("server_id", wsMsg.Message.ServerID).
					Msg("WS event: chat message")
				if wsMsg.Message.SenderID != tc.meta.UserID {
					tc.mu.Lock()
					tc.otherUsers[wsMsg.Conversation.ID] = wsMsg.Message.SenderID
					tc.mu.Unlock()
				}
				tc.dispatchMessage(&wsMsg.Conversation, wsMsg.Message)
			case evt.Reaction != nil:
				r := evt.Reaction
				log.Info().
					Str("conv_id", r.ConversationID).
					Uint64("server_message_id", r.ServerMessageID).
					Str("sender_user_id", r.SenderUserID).
					Int("modifications", len(r.Modifications)).
					Msg("WS event: reaction property update")
				tc.dispatchWSReaction(evt.Reaction)
			case evt.Deletion != nil:
				d := evt.Deletion
				log.Info().
					Str("conv_id", d.ConversationID).
					Uint64("deleted_message_id", d.DeletedMessageID).
					Str("deleter_user_id", d.DeleterUserID).
					Bool("only_for_me", d.OnlyForMe).
					Msg("WS event: message deleted on TikTok")
				tc.dispatchWSMessageDeletion(d)
			default:
				log.Warn().Msg("WS event: received event with nil Message, Reaction, and Deletion")
			}
		}

		// ch was closed — the connection dropped or ctx was cancelled.
		select {
		case <-ctx.Done():
			return
		default:
			log.Warn().Dur("retry_in", backoff).Msg("WebSocket disconnected, reconnecting")
		}
	}
}

// fetchAndDispatch is called once on Connect to backfill recent messages via
// the REST API before the WebSocket takes over.  It walks each inbox
// conversation and queues any unseen messages into the bridgev2 event pipeline,
// also populating the otherUsers cache so GetChatInfo works immediately.
//
// Deduplication relies on two layers:
//  1. The in-memory lastSeen map suppresses re-dispatches within a process lifetime.
//  2. The bridge database deduplicates by message ID across restarts.
func (tc *TikTokClient) fetchAndDispatch(ctx context.Context) error {
	log := zerolog.Ctx(ctx)

	convs, err := tc.apiClient.GetInbox(ctx)
	if err != nil {
		return fmt.Errorf("get inbox: %w", err)
	}

	log.Info().Int("conversations", len(convs)).Msg("Inbox fetched")

	if len(convs) == 0 {
		log.Info().Msg("Inbox is empty — no conversations to process")
		return nil
	}

	for i := range convs {
		conv := &convs[i]

		log.Info().
			Str("conv_id", conv.ID).
			Strs("participants", conv.Participants).
			Uint64("source_id", conv.SourceID).
			Msg("Processing conversation")

		// Identify the other participant and cache them for GetChatInfo.
		for _, pid := range conv.Participants {
			if pid != tc.meta.UserID {
				tc.mu.Lock()
				tc.otherUsers[conv.ID] = pid
				tc.mu.Unlock()
				log.Info().
					Str("conv_id", conv.ID).
					Str("other_user_id", pid).
					Str("self_user_id", tc.meta.UserID).
					Msg("Identified other participant")
				break
			}
		}

		tc.mu.Lock()
		lastSeen := tc.lastSeen[conv.ID]
		tc.mu.Unlock()

		msgs, _, err := tc.apiClient.GetMessages(ctx, conv, "")
		if err != nil {
			log.Err(err).Str("conv_id", conv.ID).Msg("Error fetching messages for conversation")
			continue
		}

		log.Info().
			Str("conv_id", conv.ID).
			Int("messages", len(msgs)).
			Int64("last_seen_ms", lastSeen).
			Msg("Fetched messages for conversation")

		var dispatched int
		for _, msg := range msgs {
			log.Info().
				Str("conv_id", conv.ID).
				Uint64("msg_id", msg.ServerID).
				Str("sender_id", msg.SenderID).
				Str("type", msg.Type).
				Int64("ts_ms", msg.TimestampMs).
				Int64("last_seen_ms", lastSeen).
				Bool("skipped", msg.TimestampMs <= lastSeen).
				Msg("Considering message")

			if msg.TimestampMs <= lastSeen {
				continue
			}
			tc.dispatchMessage(conv, msg)
			dispatched++

			if msg.TimestampMs > lastSeen {
				lastSeen = msg.TimestampMs
			}
		}

		log.Info().
			Str("conv_id", conv.ID).
			Int("dispatched", dispatched).
			Msg("Finished processing conversation")

		tc.mu.Lock()
		if lastSeen > tc.lastSeen[conv.ID] {
			tc.lastSeen[conv.ID] = lastSeen
		}
		tc.mu.Unlock()
	}
	return nil
}

// dispatchMessage queues a single TikTok message into the bridgev2 pipeline,
// followed immediately by a ReactionSync event when the message carries reactions.
func (tc *TikTokClient) dispatchMessage(conv *libtiktok.Conversation, msg libtiktok.Message) {
	tc.userLogin.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.Message[libtiktok.Message]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.
					Str("conversation_id", conv.ID).
					Uint64("message_id", msg.ServerID).
					Str("sender_id", msg.SenderID)
			},
			PortalKey: networkid.PortalKey{
				ID:       makePortalID(conv.ID),
				Receiver: tc.userLogin.ID,
			},
			CreatePortal: true,
			Sender: bridgev2.EventSender{
				IsFromMe: msg.SenderID == tc.meta.UserID,
				Sender:   makeUserID(msg.SenderID),
			},
			Timestamp: time.UnixMilli(msg.TimestampMs),
		},
		ID:                 networkid.MessageID(strconv.FormatUint(msg.ServerID, 10)),
		Data:               msg,
		ConvertMessageFunc: tc.convertMessage,
	})
	tc.dispatchReactions(conv, msg)
}

// dispatchReactions queues a ReactionSync event for all reactions on a message.
//
// QueueRemoteEvent processes events FIFO per portal, so queuing this immediately
// after the parent message guarantees the bridge has already stored the message
// by the time handleRemoteReactionSync looks it up by ID.
//
// The wire gives us reactions indexed as emoji → []userID, but ReactionSyncData
// wants the inverse: userID → []BackfillReaction. We pivot here.
func (tc *TikTokClient) dispatchReactions(conv *libtiktok.Conversation, msg libtiktok.Message) {
	if len(msg.Reactions) == 0 {
		return
	}

	// Pivot: emoji → []userID  →  userID → []BackfillReaction
	users := make(map[networkid.UserID]*bridgev2.ReactionSyncUser, len(msg.Reactions))
	for _, r := range msg.Reactions {
		emojiID := networkid.EmojiID(r.Emoji)
		for _, uid := range r.UserIDs {
			userID := makeUserID(uid)
			if users[userID] == nil {
				users[userID] = &bridgev2.ReactionSyncUser{HasAllReactions: true}
			}
			users[userID].Reactions = append(users[userID].Reactions, &bridgev2.BackfillReaction{
				Sender: bridgev2.EventSender{
					IsFromMe: uid == tc.meta.UserID,
					Sender:   userID,
				},
				EmojiID: emojiID,
				Emoji:   r.Emoji,
			})
		}
	}

	tc.userLogin.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.ReactionSync{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventReactionSync,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.
					Str("conversation_id", conv.ID).
					Uint64("message_id", msg.ServerID).
					Int("reaction_count", len(msg.Reactions))
			},
			PortalKey: networkid.PortalKey{
				ID:       makePortalID(conv.ID),
				Receiver: tc.userLogin.ID,
			},
			Sender: bridgev2.EventSender{
				IsFromMe: true,
				Sender:   makeUserID(tc.meta.UserID),
			},
			Timestamp:   time.UnixMilli(msg.TimestampMs),
			StreamOrder: 1, // must sort after the parent message at the same timestamp
		},
		TargetMessage: networkid.MessageID(strconv.FormatUint(msg.ServerID, 10)),
		Reactions: &bridgev2.ReactionSyncData{
			Users:       users,
			HasAllUsers: true,
		},
	})
}

// dispatchWSReaction queues individual RemoteEventReaction / RemoteEventReactionRemove
// events for each modification in a WebSocket property-update (type 705) event.
func (tc *TikTokClient) dispatchWSReaction(evt *libtiktok.WSReactionEvent) {
	log := tc.userLogin.Log.With().
		Str("component", "reaction-dispatch").
		Str("conv_id", evt.ConversationID).
		Uint64("server_message_id", evt.ServerMessageID).
		Logger()

	msgID := networkid.MessageID(strconv.FormatUint(evt.ServerMessageID, 10))

	senderUID := evt.SenderUserID
	if senderUID == "" {
		senderUID = tc.meta.UserID
	}

	for _, mod := range evt.Modifications {
		evtType := bridgev2.RemoteEventReaction
		if mod.Op == 1 {
			evtType = bridgev2.RemoteEventReactionRemove
		}

		log.Info().
			Str("emoji", mod.Emoji).
			Int("op", mod.Op).
			Str("sender_id", senderUID).
			Str("target_msg_id", string(msgID)).
			Str("portal_id", string(makePortalID(evt.ConversationID))).
			Str("event_type", evtType.String()).
			Msg("Queuing remote reaction event")

		tc.userLogin.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.Reaction{
			EventMeta: simplevent.EventMeta{
				Type: evtType,
				LogContext: func(c zerolog.Context) zerolog.Context {
					return c.
						Str("conversation_id", evt.ConversationID).
						Uint64("message_id", evt.ServerMessageID).
						Str("emoji", mod.Emoji).
						Str("sender_id", senderUID).
						Int("op", mod.Op)
				},
				PortalKey: networkid.PortalKey{
					ID:       makePortalID(evt.ConversationID),
					Receiver: tc.userLogin.ID,
				},
				Sender: bridgev2.EventSender{
					IsFromMe: senderUID == tc.meta.UserID,
					Sender:   makeUserID(senderUID),
				},
				Timestamp: time.Now(),
			},
			TargetMessage: msgID,
			EmojiID:       networkid.EmojiID(mod.Emoji),
			Emoji:         mod.Emoji,
		})
	}
}

// dispatchWSMessageDeletion redacts the bridged Matrix event when a message is
// removed on TikTok, either as a local hide/delete-for-self or a global recall.
func (tc *TikTokClient) dispatchWSMessageDeletion(d *libtiktok.WSMessageDeletion) {
	deleterUID := d.DeleterUserID
	if deleterUID == "" {
		deleterUID = tc.meta.UserID
	}
	msgID := networkid.MessageID(strconv.FormatUint(d.DeletedMessageID, 10))
	ts := time.UnixMilli(d.TimestampMs)
	if d.TimestampMs == 0 {
		ts = time.Now()
	}
	tc.userLogin.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.MessageRemove{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessageRemove,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.
					Str("conversation_id", d.ConversationID).
					Uint64("deleted_message_id", d.DeletedMessageID).
					Str("deleter_user_id", deleterUID)
			},
			PortalKey: networkid.PortalKey{
				ID:       makePortalID(d.ConversationID),
				Receiver: tc.userLogin.ID,
			},
			Sender: bridgev2.EventSender{
				IsFromMe: deleterUID == tc.meta.UserID,
				Sender:   makeUserID(deleterUID),
			},
			Timestamp: ts,
		},
		TargetMessage: msgID,
		OnlyForMe:     d.OnlyForMe,
	})
}

// ────────────────────────────────────────────────────────────────────────────
// Capabilities & metadata
// ────────────────────────────────────────────────────────────────────────────

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
		// handleMatrixImageMessage rejects non-empty captions for now.
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
		// Beeper uses com.beeper.room_features; without this, delete/unsend stays hidden
		// even when RedactionHandlingNetworkAPI is implemented.
		Delete: event.CapLevelFullySupported,
		// Rich replies (m.relates_to → TikTok aweType 703).
		Reply: event.CapLevelFullySupported,
		// File uploads: many clients send images or videos as m.file; advertise both.
		File: event.FileFeatureMap{
			event.MsgImage: imageUpload,
			event.MsgVideo: videoUpload,
			event.MsgFile:  matrixFileMedia,
		},
	}
}

// GetChatInfo returns Matrix room metadata for a bridged TikTok conversation.
// The other participant is read from the in-memory otherUsers cache populated
// during fetchAndDispatch. If the conversation hasn't been seen yet (e.g. a
// portal is being reconstructed from the database on startup), the portal ID
// is used as a placeholder and the room will refresh on the next poll.
func (tc *TikTokClient) GetChatInfo(_ context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	tc.mu.Lock()
	otherUserID := tc.otherUsers[string(portal.ID)]
	tc.mu.Unlock()

	if otherUserID == "" {
		// Fallback: use the portal ID itself. It's a conversation ID, not a user
		// ID, but it keeps the room functional until the poller repopulates the cache.
		otherUserID = string(portal.ID)
	}

	return &bridgev2.ChatInfo{
		Members: &bridgev2.ChatMemberList{
			IsFull: true,
			Members: []bridgev2.ChatMember{
				{
					EventSender: bridgev2.EventSender{
						IsFromMe: true,
						Sender:   makeUserID(tc.meta.UserID),
					},
					Membership: event.MembershipJoin,
					PowerLevel: ptrInt(50),
				},
				{
					EventSender: bridgev2.EventSender{
						Sender: makeUserID(otherUserID),
					},
					Membership: event.MembershipJoin,
					PowerLevel: ptrInt(50),
				},
			},
		},
	}, nil
}

// GetUserInfo fetches live profile data for a TikTok ghost user.
func (tc *TikTokClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	user, err := tc.apiClient.GetUser(ctx, string(ghost.ID))
	if err != nil {
		return nil, fmt.Errorf("get user info for %s: %w", ghost.ID, err)
	}

	name := user.Nickname
	if name == "" {
		name = "@" + user.UniqueID
	}

	return &bridgev2.UserInfo{
		Name:        &name,
		Identifiers: []string{fmt.Sprintf("tiktok:@%s", user.UniqueID)},
		Avatar:      tc.makeGhostAvatar(user),
	}, nil
}

// makeGhostAvatar builds a bridgev2.Avatar from a TikTok user profile.
// When AvatarURL is empty the returned value signals removal so that a
// previously set avatar is cleared on the Matrix side.
func (tc *TikTokClient) makeGhostAvatar(user *libtiktok.User) *bridgev2.Avatar {
	if user.AvatarURL == "" {
		return &bridgev2.Avatar{
			ID:     "remove",
			Remove: true,
		}
	}
	avatarURL := user.AvatarURL // capture for the closure
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(avatarURL),
		Get: func(ctx context.Context) ([]byte, error) {
			return tc.apiClient.DownloadAvatar(ctx, avatarURL)
		},
	}
}

// fetchGhostAvatar fetches the latest avatar for ghost from TikTok and
// applies it via ghost.UpdateAvatar.  It returns true when the avatar was
// actually changed, matching the convention used by other mautrix bridges.
func (tc *TikTokClient) fetchGhostAvatar(ctx context.Context, ghost *bridgev2.Ghost) bool {
	user, err := tc.apiClient.GetUser(ctx, string(ghost.ID))
	if err != nil {
		zerolog.Ctx(ctx).Err(err).
			Str("ghost_id", string(ghost.ID)).
			Msg("Failed to get user info for avatar update")
		return false
	}
	return ghost.UpdateAvatar(ctx, tc.makeGhostAvatar(user))
}

// ────────────────────────────────────────────────────────────────────────────
// Matrix → TikTok
// ────────────────────────────────────────────────────────────────────────────

// HandleMatrixMessage forwards a Matrix message to the TikTok conversation.
func (tc *TikTokClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	log := zerolog.Ctx(ctx)

	content := *msg.Content
	content.RemoveReplyFallback()

	reply := tc.buildOutgoingReply(log, msg)

	conv, err := tc.getConversationForPortal(ctx, msg.Portal)
	if err != nil {
		return nil, err
	}

	if content.MsgType == event.MsgImage {
		return tc.handleMatrixImageMessage(ctx, msg, conv, &content, reply)
	}
	if content.MsgType == event.MsgVideo {
		return tc.handleMatrixVideoMessage(ctx, msg, conv, &content, reply)
	}
	if content.MsgType == event.MsgFile {
		mimeType := ""
		if content.Info != nil {
			mimeType = content.Info.MimeType
		}
		switch {
		case strings.HasPrefix(mimeType, "image/"):
			return tc.handleMatrixImageMessage(ctx, msg, conv, &content, reply)
		case strings.HasPrefix(mimeType, "video/"):
			return tc.handleMatrixVideoMessage(ctx, msg, conv, &content, reply)
		case mimeType == "":
			if resp, err := tc.handleMatrixImageMessage(ctx, msg, conv, &content, reply); err == nil {
				return resp, nil
			} else if !errors.Is(err, bridgev2.ErrUnsupportedMessageType) {
				return nil, err
			}
			return tc.handleMatrixVideoMessage(ctx, msg, conv, &content, reply)
		default:
			return nil, bridgev2.ErrUnsupportedMessageType
		}
	}

	text, err := matrixToTikTok(&content)
	if err != nil {
		return nil, err
	}

	resp, err := tc.apiClient.SendMessage(ctx, libtiktok.SendMessageParams{
		ConvID:       conv.ID,
		ConvSourceID: conv.SourceID,
		Text:         text,
		IsGroup:      conv.ConversationType == 2,
		Reply:        reply,
	})
	if err != nil {
		return nil, fmt.Errorf("send TikTok message: %w", err)
	}

	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:       networkid.MessageID(resp.MessageID),
			SenderID: makeUserID(tc.meta.UserID),
		},
	}, nil
}

func (tc *TikTokClient) handleMatrixImageMessage(
	ctx context.Context,
	msg *bridgev2.MatrixMessage,
	conv *libtiktok.Conversation,
	content *event.MessageEventContent,
	reply *libtiktok.OutgoingMessageReply,
) (*bridgev2.MatrixMessageResponse, error) {
	if strings.TrimSpace(content.GetCaption()) != "" {
		return nil, fmt.Errorf("image captions are not yet supported on TikTok")
	}

	matrix := tc.userLogin.Bridge.Matrix.BotIntent()
	data, err := matrix.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
	}

	mimeType := ""
	if content.Info != nil {
		mimeType = content.Info.MimeType
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return nil, bridgev2.ErrUnsupportedMessageType
	}

	resp, err := tc.apiClient.SendMessage(ctx, libtiktok.SendMessageParams{
		ConvID:       conv.ID,
		ConvSourceID: conv.SourceID,
		IsGroup:      conv.ConversationType == 2,
		Reply:        reply,
		Image: &libtiktok.OutgoingImage{
			Data:     data,
			FileName: content.GetFileName(),
			MimeType: mimeType,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("send TikTok image: %w", err)
	}

	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:       networkid.MessageID(resp.MessageID),
			SenderID: makeUserID(tc.meta.UserID),
		},
	}, nil
}

func (tc *TikTokClient) handleMatrixVideoMessage(
	ctx context.Context,
	msg *bridgev2.MatrixMessage,
	conv *libtiktok.Conversation,
	content *event.MessageEventContent,
	reply *libtiktok.OutgoingMessageReply,
) (*bridgev2.MatrixMessageResponse, error) {
	if strings.TrimSpace(content.GetCaption()) != "" {
		return nil, fmt.Errorf("video captions are not yet supported on TikTok")
	}

	matrix := tc.userLogin.Bridge.Matrix.BotIntent()
	data, err := matrix.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
	}

	mimeType := ""
	if content.Info != nil {
		mimeType = content.Info.MimeType
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mimeType, "video/") {
		return nil, bridgev2.ErrUnsupportedMessageType
	}

	resp, err := tc.apiClient.SendMessage(ctx, libtiktok.SendMessageParams{
		ConvID:       conv.ID,
		ConvSourceID: conv.SourceID,
		IsGroup:      conv.ConversationType == 2,
		Reply:        reply,
		Video: &libtiktok.OutgoingVideo{
			Data:     data,
			FileName: content.GetFileName(),
			MimeType: mimeType,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("send TikTok video: %w", err)
	}

	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:       networkid.MessageID(resp.MessageID),
			SenderID: makeUserID(tc.meta.UserID),
		},
	}, nil
}

func (tc *TikTokClient) buildOutgoingReply(log *zerolog.Logger, msg *bridgev2.MatrixMessage) *libtiktok.OutgoingMessageReply {
	if msg.ReplyTo == nil {
		return nil
	}
	if msg.ReplyTo.Room != msg.Portal.PortalKey {
		log.Debug().Msg("Matrix reply target is in another portal; sending without TikTok reply envelope")
		return nil
	}

	parentID, perr := strconv.ParseUint(string(msg.ReplyTo.ID), 10, 64)
	if perr != nil {
		log.Debug().Err(perr).Str("parent_id", string(msg.ReplyTo.ID)).
			Msg("Matrix reply target is not a TikTok server message id; sending without TikTok reply envelope")
		return nil
	}
	if parentID == 0 {
		log.Debug().Str("parent_id", string(msg.ReplyTo.ID)).
			Msg("Matrix reply target has zero id; sending without TikTok reply envelope")
		return nil
	}

	var pm *MessageMetadata
	if raw, ok := msg.ReplyTo.Metadata.(*MessageMetadata); ok {
		pm = raw
	}
	refUID := string(msg.ReplyTo.SenderID)
	refSec := ""
	chainID := uint64(0)
	cursorUs := uint64(0)
	contentJSON := ""
	if pm != nil {
		refSec = pm.SenderSecUID
		chainID = pm.SendChainID
		cursorUs = pm.CursorTsUs
		contentJSON = pm.ContentJSON
	}
	if cursorUs == 0 {
		cursorUs = uint64(msg.ReplyTo.Timestamp.UnixMicro())
	}

	refBytes, err := libtiktok.BuildReplyReferenceJSON(contentJSON, refUID, refSec)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to build TikTok reply reference JSON; sending as non-reply")
		return nil
	}
	return &libtiktok.OutgoingMessageReply{
		ParentServerMessageID: parentID,
		ParentSendChainID:     chainID,
		ParentCursorTsUs:      cursorUs,
		ReferencePayloadJSON:  refBytes,
	}
}

// HandleMatrixMessageRemove recalls the message on TikTok (delete for everyone)
// when the redacted Matrix event corresponds to a message we sent. Other
// messages are left untouched on TikTok (Matrix redaction still completes on
// the bridge side).
func (tc *TikTokClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	if msg.TargetMessage == nil {
		return fmt.Errorf("nil redaction target message")
	}
	if msg.TargetMessage.SenderID != makeUserID(tc.meta.UserID) {
		zerolog.Ctx(ctx).Debug().
			Str("target_sender", string(msg.TargetMessage.SenderID)).
			Msg("Skipping TikTok delete: redacted message was not sent by this login")
		return nil
	}

	serverMessageID, err := strconv.ParseUint(string(msg.TargetMessage.ID), 10, 64)
	if err != nil {
		return fmt.Errorf("cannot delete on TikTok: bridged message id %q is not a numeric server message id: %w", msg.TargetMessage.ID, err)
	}

	conv, err := tc.getConversationForPortal(ctx, msg.Portal)
	if err != nil {
		return err
	}

	err = tc.apiClient.RecallMessage(ctx, libtiktok.DeleteMessageParams{
		ConvID:          conv.ID,
		ConvoSourceID:   conv.SourceID,
		ServerMessageID: serverMessageID,
	})
	if err != nil {
		return fmt.Errorf("recall TikTok message: %w", err)
	}
	return nil
}

// PreHandleMatrixReaction extracts the Matrix reaction key and maps it to the
// current TikTok login so bridgev2 can deduplicate outgoing reactions.
func (tc *TikTokClient) PreHandleMatrixReaction(_ context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	emoji := msg.Content.RelatesTo.GetAnnotationKey()
	if emoji == "" {
		return bridgev2.MatrixReactionPreResponse{}, fmt.Errorf("missing Matrix reaction annotation key")
	}

	return bridgev2.MatrixReactionPreResponse{
		SenderID: makeUserID(tc.meta.UserID),
		EmojiID:  networkid.EmojiID(emoji),
		Emoji:    emoji,
	}, nil
}

// HandleMatrixReaction forwards a Matrix reaction to TikTok using the existing
// add-reaction API.
func (tc *TikTokClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	serverMessageID, err := strconv.ParseUint(string(msg.TargetMessage.ID), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse TikTok message ID %q: %w", msg.TargetMessage.ID, err)
	}

	conv, err := tc.getConversationForPortal(ctx, msg.Portal)
	if err != nil {
		return nil, err
	}

	emoji := msg.PreHandleResp.Emoji
	if emoji == "" {
		emoji = msg.Content.RelatesTo.GetAnnotationKey()
	}

	err = tc.apiClient.SendReaction(ctx, libtiktok.SendReactionParams{
		ConvID:          conv.ID,
		IsGroup:         conv.ConversationType == 2,
		Emoji:           emoji,
		Action:          libtiktok.ReactionAdd,
		SelfUserID:      tc.meta.UserID,
		ConvoSourceID:   conv.SourceID,
		ServerMessageID: serverMessageID,
	})
	if err != nil {
		return nil, fmt.Errorf("send TikTok reaction: %w", err)
	}

	return &database.Reaction{
		SenderID: makeUserID(tc.meta.UserID),
		EmojiID:  networkid.EmojiID(emoji),
		Emoji:    emoji,
	}, nil
}

// HandleMatrixReactionRemove removes a previously bridged reaction from TikTok.
func (tc *TikTokClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	serverMessageID, err := strconv.ParseUint(string(msg.TargetReaction.MessageID), 10, 64)
	if err != nil {
		return fmt.Errorf("parse TikTok message ID %q: %w", msg.TargetReaction.MessageID, err)
	}

	conv, err := tc.getConversationForPortal(ctx, msg.Portal)
	if err != nil {
		return err
	}

	emoji := msg.TargetReaction.Emoji
	if emoji == "" {
		emoji = string(msg.TargetReaction.EmojiID)
	}
	if emoji == "" {
		return fmt.Errorf("reaction %s has no TikTok emoji key", msg.TargetReaction.MXID)
	}

	err = tc.apiClient.SendReaction(ctx, libtiktok.SendReactionParams{
		ConvID:          conv.ID,
		IsGroup:         conv.ConversationType == 2,
		Emoji:           emoji,
		Action:          libtiktok.ReactionRemove,
		SelfUserID:      tc.meta.UserID,
		ConvoSourceID:   conv.SourceID,
		ServerMessageID: serverMessageID,
	})
	if err != nil {
		return fmt.Errorf("remove TikTok reaction: %w", err)
	}

	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Identifier resolution
// ────────────────────────────────────────────────────────────────────────────

// ResolveIdentifier will enable the `start-chat` bot command once
// libtiktok.GetUserByUsername is implemented.
func (tc *TikTokClient) ResolveIdentifier(_ context.Context, identifier string, _ bool) (*bridgev2.ResolveIdentifierResponse, error) {
	return nil, fmt.Errorf("start-chat not yet available: GetUserByUsername is not implemented (got %q)", identifier)
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

func (tc *TikTokClient) getConversationForPortal(ctx context.Context, portal *bridgev2.Portal) (*libtiktok.Conversation, error) {
	convID := string(portal.ID)
	meta, _ := portal.Metadata.(*PortalMetadata)
	if meta != nil {
		if meta.ConversationID != "" {
			convID = meta.ConversationID
		}
		// Require conversation_type before skipping the inbox fetch. Older bridge
		// versions cached source_id only; those portals stayed at type 0 in RAM
		// even after the DB row gained conversation_type:2, so group flags
		// (reactions, send envelope) were wrong until the next full metadata refresh.
		if meta.SourceID != 0 && meta.ConversationType != 0 {
			return &libtiktok.Conversation{
				ID:               convID,
				SourceID:         meta.SourceID,
				ConversationType: meta.ConversationType,
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

		if meta == nil {
			meta = &PortalMetadata{}
			portal.Metadata = meta
		}
		meta.ConversationID = convs[i].ID
		meta.SourceID = convs[i].SourceID
		meta.ConversationType = convs[i].ConversationType
		if err := portal.Save(ctx); err != nil {
			return nil, fmt.Errorf("cache TikTok portal metadata: %w", err)
		}

		return &convs[i], nil
	}

	return nil, fmt.Errorf("TikTok conversation %q not found in inbox", convID)
}

func ptrInt(v int) *int { return &v }
