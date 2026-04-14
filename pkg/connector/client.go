package connector

import (
	"context"
	"fmt"
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

	stopPolling context.CancelFunc
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

// Connect validates the TikTok session and starts the background polling loop.
// Errors are reported via BridgeState rather than returned (mautrix-go March 2025).
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
		Msg("TikTok session validated, starting poller")

	tc.isConnected = true
	tc.userLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateConnected,
	})

	// Fresh background context so the loop outlives the Connect call's context.
	pollCtx, cancel := context.WithCancel(context.Background())
	tc.stopPolling = cancel
	go tc.pollLoop(pollCtx)
}

// Disconnect stops the polling loop.
func (tc *TikTokClient) Disconnect() {
	if tc.stopPolling != nil {
		tc.stopPolling()
		tc.stopPolling = nil
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
// Polling
// ────────────────────────────────────────────────────────────────────────────

func (tc *TikTokClient) pollLoop(ctx context.Context) {
	log := tc.userLogin.Log.With().Str("component", "tiktok-poller").Logger()
	// Attach the logger to the context so zerolog.Ctx(ctx) works inside
	// fetchAndDispatch and any libtiktok calls that log through it.
	ctx = log.WithContext(ctx)

	log.Info().Msg("Starting TikTok message poller")
	defer log.Info().Msg("TikTok message poller stopped")

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := tc.fetchAndDispatch(ctx); err != nil {
				log.Err(err).Msg("Error fetching TikTok messages")
			}
		}
	}
}

// fetchAndDispatch fetches the inbox, walks each conversation for new messages,
// and queues any it hasn't seen yet into the bridgev2 event pipeline.
//
// Deduplication relies on two layers:
//  1. The in-memory lastSeen map filters within a single process lifetime.
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
			Str("source_id", conv.SourceID).
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
				Str("msg_id", msg.ID).
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

// dispatchMessage queues a single TikTok message into the bridgev2 pipeline.
func (tc *TikTokClient) dispatchMessage(conv *libtiktok.Conversation, msg libtiktok.Message) {
	tc.userLogin.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.Message[libtiktok.Message]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.
					Str("conversation_id", conv.ID).
					Str("message_id", msg.ID).
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
		ID:                 networkid.MessageID(msg.ID),
		Data:               msg,
		ConvertMessageFunc: tc.convertMessage,
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

	info := &bridgev2.UserInfo{
		Name:        &name,
		Identifiers: []string{fmt.Sprintf("tiktok:@%s", user.UniqueID)},
	}

	// TODO: proxy the avatar through Matrix once media upload is wired in, e.g.:
	//   if user.AvatarURL != "" {
	//       data, mime, err := downloadHTTP(ctx, user.AvatarURL)
	//       if err == nil {
	//           mxc, _, err := intent.UploadMedia(ctx, "", data, user.UniqueID+".jpg", mime)
	//           if err == nil {
	//               info.Avatar = &bridgev2.Avatar{MXC: mxc}
	//           }
	//       }
	//   }

	return info, nil
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
