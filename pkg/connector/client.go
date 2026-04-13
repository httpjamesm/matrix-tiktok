package connector

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
)

// TikTokClient implements bridgev2.NetworkAPI for a single logged-in TikTok session.
type TikTokClient struct {
	connector *TikTokConnector
	userLogin *bridgev2.UserLogin
	meta      *UserLoginMetadata

	// stopPolling cancels the background polling goroutine started in Connect.
	stopPolling context.CancelFunc
	isConnected bool
}

// Ensure TikTokClient fully implements the required interfaces.
var _ bridgev2.NetworkAPI = (*TikTokClient)(nil)
var _ bridgev2.IdentifierResolvingNetworkAPI = (*TikTokClient)(nil)

// ────────────────────────────────────────────────────────────────────────────
// Connection lifecycle
// ────────────────────────────────────────────────────────────────────────────

// Connect is called when the bridge wants to bring the user login online.
// For TikTok (which has no official webhook support) we start a polling loop here.
// Any connection errors must be reported through BridgeState.Send rather than
// returned, per the March 2025 mautrix-go API update.
func (tc *TikTokClient) Connect(ctx context.Context) {
	log := zerolog.Ctx(ctx)

	// TODO: Wire in your PoC TikTok API client here to validate the session, e.g.:
	//
	//   apiClient := tiktokapi.NewClient(tc.meta.Cookies)
	//   _, err := apiClient.GetSelf(ctx)
	//   if err != nil {
	//       tc.userLogin.BridgeState.Send(status.BridgeState{
	//           StateEvent: status.StateBadCredentials,
	//           Error:      "tiktok-auth-error",
	//           Message:    "TikTok session is no longer valid — please log in again",
	//           Info:        map[string]any{"go_error": err.Error()},
	//       })
	//       return
	//   }
	//   tc.apiClient = apiClient

	log.Info().
		Str("user_id", tc.meta.UserID).
		Str("username", tc.meta.Username).
		Msg("Connecting TikTok client")

	tc.isConnected = true
	tc.userLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateConnected,
	})

	// Start the polling goroutine. We pass a fresh background context so that
	// the loop is not tied to the lifetime of the Connect call's context.
	pollCtx, cancel := context.WithCancel(context.Background())
	tc.stopPolling = cancel
	go tc.pollLoop(pollCtx)
}

// Disconnect tears down the remote connection for this login.
func (tc *TikTokClient) Disconnect() {
	if tc.stopPolling != nil {
		tc.stopPolling()
		tc.stopPolling = nil
	}
	tc.isConnected = false
}

// IsLoggedIn reports whether the login is currently considered valid.
// This must NOT do any IO — return a cached value only.
func (tc *TikTokClient) IsLoggedIn() bool {
	return tc.isConnected
}

// LogoutRemote invalidates the remote TikTok session.
func (tc *TikTokClient) LogoutRemote(ctx context.Context) {
	// TODO: Call your PoC API client's logout endpoint if one exists, e.g.:
	//   _ = tc.apiClient.Logout(ctx)
}

// ────────────────────────────────────────────────────────────────────────────
// Polling
// ────────────────────────────────────────────────────────────────────────────

// pollLoop runs in a background goroutine and periodically fetches new
// messages from the TikTok inbox until ctx is cancelled.
func (tc *TikTokClient) pollLoop(ctx context.Context) {
	log := tc.userLogin.Log.With().Str("component", "tiktok-poller").Logger()
	log.Info().Msg("Starting TikTok message poller")
	defer log.Info().Msg("TikTok message poller stopped")

	// TODO: Tune the poll interval. TikTok's unofficial API may impose rate limits.
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := tc.fetchAndDispatch(ctx); err != nil {
				log.Err(err).Msg("Error while fetching TikTok messages")
			}
		}
	}
}

// fetchAndDispatch retrieves new messages from the TikTok inbox and queues
// each one as a remote event for the central bridge module to process.
func (tc *TikTokClient) fetchAndDispatch(ctx context.Context) error {
	// TODO: Call your PoC API client to retrieve the inbox, e.g.:
	//
	//   conversations, err := tc.apiClient.GetInbox(ctx)
	//   if err != nil {
	//       return fmt.Errorf("get inbox: %w", err)
	//   }
	//   for _, conv := range conversations {
	//       for _, msg := range conv.NewMessages {
	//           tc.dispatchMessage(ctx, conv.ID, msg.SenderID, msg.ID,
	//               time.Unix(msg.Timestamp, 0), msg)
	//       }
	//   }

	zerolog.Ctx(ctx).Trace().Msg("fetchAndDispatch: not yet implemented — wire in your PoC here")
	return nil
}

// dispatchMessage queues a single TikTok message into the bridgev2 event pipeline.
// Once your PoC client is wired in, replace *TikTokMessage with its concrete message
// type and update convertMessage accordingly.
func (tc *TikTokClient) dispatchMessage(
	_ context.Context,
	conversationID string,
	senderID string,
	messageID string,
	timestamp time.Time,
	data *TikTokMessage,
) {
	tc.userLogin.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.Message[*TikTokMessage]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.
					Str("conversation_id", conversationID).
					Str("message_id", messageID).
					Str("sender_id", senderID)
			},
			PortalKey: networkid.PortalKey{
				ID:       makePortalID(conversationID),
				Receiver: tc.userLogin.ID,
			},
			CreatePortal: true,
			Sender: bridgev2.EventSender{
				IsFromMe: senderID == tc.meta.UserID,
				Sender:   makeUserID(senderID),
			},
			Timestamp: timestamp,
		},
		ID:                 networkid.MessageID(messageID),
		Data:               data,
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

// GetCapabilities returns the Matrix room feature-set that this login supports.
func (tc *TikTokClient) GetCapabilities(_ context.Context, _ *bridgev2.Portal) *event.RoomFeatures {
	return &event.RoomFeatures{
		// TikTok DM text limit is not publicly documented; 1 000 chars is conservative.
		MaxTextLength: 1000,
		// TODO: add image/video/sticker upload capabilities once media bridging is done.
	}
}

// GetChatInfo returns Matrix room metadata for a bridged TikTok conversation.
// For 1-on-1 DMs the portal ID is the TikTok conversation ID.
func (tc *TikTokClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	// TODO: Fetch live conversation metadata from the TikTok API, e.g.:
	//
	//   conv, err := tc.apiClient.GetConversation(ctx, string(portal.ID))
	//   if err != nil {
	//       return nil, fmt.Errorf("get conversation info: %w", err)
	//   }
	//   name := ptr.Ptr(conv.Title) // non-nil only for group chats

	return &bridgev2.ChatInfo{
		Members: &bridgev2.ChatMemberList{
			IsFull: true,
			Members: []bridgev2.ChatMember{
				{
					// The Matrix user's own side of the DM.
					EventSender: bridgev2.EventSender{
						IsFromMe: true,
						Sender:   makeUserID(tc.meta.UserID),
					},
					Membership: event.MembershipJoin,
					PowerLevel: ptrInt(50),
				},
				{
					// The remote TikTok user represented as a ghost.
					EventSender: bridgev2.EventSender{
						Sender: networkid.UserID(portal.ID),
					},
					Membership: event.MembershipJoin,
					PowerLevel: ptrInt(50),
				},
			},
		},
	}, nil
}

// GetUserInfo returns profile metadata for a TikTok ghost user.
func (tc *TikTokClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	// TODO: Fetch real user info from the TikTok API, e.g.:
	//
	//   user, err := tc.apiClient.GetUser(ctx, string(ghost.ID))
	//   if err != nil {
	//       return nil, fmt.Errorf("get user info: %w", err)
	//   }
	//   // Re-upload the avatar to Matrix:
	//   // avatarMXC, err := intent.UploadMedia(ctx, ...)
	//   return &bridgev2.UserInfo{
	//       Name:        &user.Nickname,
	//       Avatar:      &bridgev2.Avatar{MXC: avatarMXC},
	//       Identifiers: []string{fmt.Sprintf("tiktok:@%s", user.UniqueID)},
	//   }, nil

	return &bridgev2.UserInfo{
		Identifiers: []string{fmt.Sprintf("tiktok:%s", ghost.ID)},
	}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Matrix → TikTok
// ────────────────────────────────────────────────────────────────────────────

// HandleMatrixMessage is called when a Matrix user sends a message in a bridged room.
// It forwards the message to TikTok and returns the resulting remote message ID.
func (tc *TikTokClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	// TODO: Forward the message via your PoC API client, e.g.:
	//
	//   resp, err := tc.apiClient.SendMessage(ctx, tiktokapi.SendMessageParams{
	//       ConversationID: string(msg.Portal.ID),
	//       Text:           msg.Content.Body,
	//   })
	//   if err != nil {
	//       return nil, fmt.Errorf("send tiktok message: %w", err)
	//   }
	//   return &bridgev2.MatrixMessageResponse{
	//       DB: &database.Message{
	//           ID:       networkid.MessageID(resp.MessageID),
	//           SenderID: makeUserID(tc.meta.UserID),
	//       },
	//   }, nil

	return nil, fmt.Errorf("HandleMatrixMessage: not yet implemented")
}

// ────────────────────────────────────────────────────────────────────────────
// Identifier resolution (enables `start-chat` bot command)
// ────────────────────────────────────────────────────────────────────────────

// ResolveIdentifier resolves a TikTok username or numeric user ID into the
// corresponding ghost and portal objects, enabling the `resolve-identifier`
// and `start-chat` bridge bot commands.
func (tc *TikTokClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	// TODO: Validate and look up the identifier via your PoC API client, e.g.:
	//
	//   user, err := tc.apiClient.GetUserByUsername(ctx, strings.TrimPrefix(identifier, "@"))
	//   if err != nil {
	//       return nil, fmt.Errorf("could not resolve TikTok user %q: %w", identifier, err)
	//   }
	//   userID  := makeUserID(user.ID)
	//   portalKey := networkid.PortalKey{
	//       ID:       makePortalID(user.ConversationID),
	//       Receiver: tc.userLogin.ID,
	//   }
	//   ghost, err := tc.userLogin.Bridge.GetGhostByID(ctx, userID)
	//   if err != nil {
	//       return nil, fmt.Errorf("get ghost: %w", err)
	//   }
	//   portal, err := tc.userLogin.Bridge.GetPortalByKey(ctx, portalKey)
	//   if err != nil {
	//       return nil, fmt.Errorf("get portal: %w", err)
	//   }
	//   ghostInfo,  _ := tc.GetUserInfo(ctx, ghost)
	//   portalInfo, _ := tc.GetChatInfo(ctx, portal)
	//   return &bridgev2.ResolveIdentifierResponse{
	//       Ghost:    ghost,
	//       UserID:   userID,
	//       UserInfo: ghostInfo,
	//       Chat: &bridgev2.CreateChatResponse{
	//           Portal:     portal,
	//           PortalKey:  portalKey,
	//           PortalInfo: portalInfo,
	//       },
	//   }, nil

	return nil, fmt.Errorf("ResolveIdentifier: not yet implemented")
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// ptrInt returns a pointer to the given int value (used for PowerLevel fields).
func ptrInt(v int) *int { return &v }
