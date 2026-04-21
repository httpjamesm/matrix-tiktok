package connector

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2/status"
)

const connectSingleflightKey = "tiktok-connect"

// Connect validates the TikTok session, performs a one-time REST backfill to
// catch up on recent messages, then starts the WebSocket loop for real-time
// delivery. Errors are reported via BridgeState rather than returned
// (mautrix-go March 2025 convention).
func (tc *TikTokClient) Connect(ctx context.Context) {
	_, _, _ = tc.connectFlight.Do(connectSingleflightKey, func() (any, error) {
		tc.connectOnce(ctx)
		return nil, nil
	})
}

func (tc *TikTokClient) connectOnce(ctx context.Context) {
	log := zerolog.Ctx(ctx).With().Str("component", "connector-lifecycle").Logger()
	ctx = log.WithContext(ctx)

	if tc.isConnected && tc.stopLoop != nil {
		log.Debug().Msg("Skipping Connect: session already active")
		return
	}

	if _, err := tc.apiClient.GetSelfWithRetry(ctx, 5); err != nil {
		tc.sendGetSelfBridgeState(err)
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
	log.Debug().Msg("Starting initial REST backfill before websocket connect")
	if err := tc.fetchAndDispatch(ctx); err != nil {
		log.Warn().Err(err).Msg("Initial REST backfill failed, proceeding to WebSocket anyway")
	} else {
		log.Debug().Msg("Initial REST backfill completed")
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

// sendGetSelfBridgeState maps GetSelf failures to bridge state. Many failures are
// flaky HTML/hydration responses from /messages (see GetSelfWithRetry) and are
// not invalid cookies — those must not use BAD_CREDENTIALS.
func (tc *TikTokClient) sendGetSelfBridgeState(err error) {
	msg := err.Error()
	st := status.BridgeState{
		Error: "tiktok-session-error",
		Info:  map[string]any{"go_error": msg},
	}
	switch {
	case strings.Contains(msg, "unexpected status 401"),
		strings.Contains(msg, "unexpected status 403"):
		st.StateEvent = status.StateBadCredentials
		st.Message = "TikTok session is no longer valid — please log in again"
	default:
		st.StateEvent = status.StateUnknownError
		st.Message = "TikTok session check failed (temporary or unclear error) — try again or restart the bridge"
	}
	tc.userLogin.BridgeState.Send(st)
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

// wsLoop dials the TikTok IM WebSocket and dispatches incoming chat events
// until ctx is cancelled. On disconnect it waits with exponential back-off
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
				log.Debug().Dur("next_retry_in", backoff).Msg("WebSocket reconnect backoff updated")
				continue
			}
		}
		backoff = 5 * time.Second

		log.Info().Msg("WebSocket connected, listening for messages")

		for evt := range ch {
			switch {
			case evt.Message != nil:
				wsMsg := evt.Message
				log.Debug().
					Str("conversation_id", wsMsg.Conversation.ID).
					Str("sender_id", wsMsg.Message.SenderID).
					Str("type", wsMsg.Message.Type).
					Uint64("server_message_id", wsMsg.Message.ServerID).
					Msg("WS event: chat message")
				if wsMsg.Message.SenderID != tc.meta.UserID {
					tc.mu.Lock()
					tc.otherUsers[wsMsg.Conversation.ID] = wsMsg.Message.SenderID
					tc.mu.Unlock()
					log.Debug().
						Str("conversation_id", wsMsg.Conversation.ID).
						Str("other_user_id", wsMsg.Message.SenderID).
						Msg("Updated other-user cache from websocket message")
				}
				tc.dispatchMessage(&wsMsg.Conversation, wsMsg.Message)
			case evt.Reaction != nil:
				r := evt.Reaction
				log.Debug().
					Str("conversation_id", r.ConversationID).
					Uint64("server_message_id", r.ServerMessageID).
					Str("sender_id", r.SenderUserID).
					Int("modifications", len(r.Modifications)).
					Msg("WS event: reaction property update")
				tc.dispatchWSReaction(evt.Reaction)
			case evt.Deletion != nil:
				d := evt.Deletion
				log.Debug().
					Str("conversation_id", d.ConversationID).
					Uint64("message_id", d.DeletedMessageID).
					Str("sender_id", d.DeleterUserID).
					Bool("only_for_me", d.OnlyForMe).
					Msg("WS event: message deleted on TikTok")
				tc.dispatchWSMessageDeletion(d)
			case evt.ReadReceipt != nil:
				rr := evt.ReadReceipt
				log.Debug().
					Str("conversation_id", rr.ConversationID).
					Uint64("server_message_id", rr.ReadServerMessageID).
					Str("reader_user_id", rr.ReaderUserID).
					Msg("WS event: read receipt")
				tc.dispatchWSReadReceipt(rr)
			default:
				log.Warn().Msg("WS event: received event with nil Message, Reaction, Deletion, and ReadReceipt")
			}
		}

		select {
		case <-ctx.Done():
			return
		default:
			log.Warn().Dur("retry_in", backoff).Msg("WebSocket disconnected, reconnecting")
		}
	}
}
