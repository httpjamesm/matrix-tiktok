package connector

import (
	"context"
	"fmt"
	"strconv"
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

		for wsMsg := range ch {
			// Update the otherUsers cache so GetChatInfo can build the room
			// membership list.  For messages from others the sender IS the
			// other participant.  For echoes of our own messages the cache
			// was already populated by the initial fetchAndDispatch backfill.
			if wsMsg.Message.SenderID != tc.meta.UserID {
				tc.mu.Lock()
				tc.otherUsers[wsMsg.Conversation.ID] = wsMsg.Message.SenderID
				tc.mu.Unlock()
			}
			tc.dispatchMessage(&wsMsg.Conversation, wsMsg.Message)
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

// ────────────────────────────────────────────────────────────────────────────
// Capabilities & metadata
// ────────────────────────────────────────────────────────────────────────────

// IsThisUser reports whether the given remote user ID belongs to this login.
func (tc *TikTokClient) IsThisUser(_ context.Context, userID networkid.UserID) bool {
	return makeUserID(tc.meta.UserID) == userID
}

// GetCapabilities returns the Matrix room feature-set for this login.
func (tc *TikTokClient) GetCapabilities(_ context.Context, _ *bridgev2.Portal) *event.RoomFeatures {
	return &event.RoomFeatures{
		MaxTextLength: 1000,
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
	text, err := matrixToTikTok(msg.Content)
	if err != nil {
		return nil, err
	}

	resp, err := tc.apiClient.SendMessage(ctx, libtiktok.SendMessageParams{
		ConvID: string(msg.Portal.ID),
		Text:   text,
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

func ptrInt(v int) *int { return &v }
