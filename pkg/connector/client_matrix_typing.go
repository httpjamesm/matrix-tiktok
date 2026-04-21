package connector

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

func (tc *TikTokClient) sendTyping(ctx context.Context, params libtiktok.SendTypingParams) error {
	if tc.sendTypingForTest != nil {
		return tc.sendTypingForTest(ctx, params)
	}
	return tc.apiClient.SendTyping(ctx, params)
}

// HandleMatrixTyping forwards Matrix typing state into TikTok's typing heartbeat API.
func (tc *TikTokClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	log := zerolog.Ctx(ctx).With().Str("component", "connector-matrix-typing").Logger()
	ctx = log.WithContext(ctx)
	if msg == nil || msg.Portal == nil {
		return nil
	}
	if msg.Type != bridgev2.TypingTypeText {
		log.Debug().
			Str("portal_id", string(msg.Portal.ID)).
			Int("typing_type", int(msg.Type)).
			Msg("Ignoring unsupported Matrix typing type for TikTok")
		return nil
	}

	portalKey := string(msg.Portal.ID)
	if !msg.IsTyping {
		tc.stopOutgoingTypingLoop(portalKey)
		log.Debug().Str("portal_id", portalKey).Msg("Stopped outbound TikTok typing heartbeat loop")
		return nil
	}

	conv, err := tc.getConversationForPortal(ctx, msg.Portal)
	if err != nil {
		return fmt.Errorf("resolve TikTok conversation for typing: %w", err)
	}
	params := libtiktok.SendTypingParams{
		ConvID:       conv.ID,
		ConvSourceID: conv.SourceID,
	}
	tc.startOutgoingTypingLoop(ctx, portalKey, params)
	return nil
}

func (tc *TikTokClient) startOutgoingTypingLoop(ctx context.Context, portalKey string, params libtiktok.SendTypingParams) {
	interval := tc.outboundTypingInterval
	if interval <= 0 {
		interval = outboundTypingHeartbeatInterval
	}
	log := zerolog.Ctx(ctx).With().
		Str("component", "connector-matrix-typing").
		Str("portal_id", portalKey).
		Str("conversation_id", params.ConvID).
		Uint64("source_id", params.ConvSourceID).
		Dur("interval", interval).
		Logger()

	tc.mu.Lock()
	if tc.outboundTyping[portalKey] != nil {
		tc.mu.Unlock()
		log.Debug().Msg("Outbound TikTok typing heartbeat loop already running")
		return
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	state := &outboundTypingState{cancel: cancel}
	tc.outboundTyping[portalKey] = state
	tc.mu.Unlock()

	go func() {
		defer func() {
			tc.mu.Lock()
			if tc.outboundTyping[portalKey] == state {
				delete(tc.outboundTyping, portalKey)
			}
			tc.mu.Unlock()
		}()

		send := func() {
			sendCtx, cancel := context.WithTimeout(loopCtx, interval)
			defer cancel()
			if err := tc.sendTyping(sendCtx, params); err != nil {
				log.Warn().Err(err).Msg("Failed to send TikTok typing heartbeat")
				return
			}
			log.Debug().Msg("Sent TikTok typing heartbeat")
		}

		send()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				log.Debug().Msg("Outbound TikTok typing heartbeat loop stopped")
				return
			case <-ticker.C:
				send()
			}
		}
	}()
}

func (tc *TikTokClient) stopOutgoingTypingLoop(portalKey string) {
	tc.mu.Lock()
	state := tc.outboundTyping[portalKey]
	if state != nil {
		delete(tc.outboundTyping, portalKey)
	}
	tc.mu.Unlock()
	if state != nil && state.cancel != nil {
		state.cancel()
	}
}

func (tc *TikTokClient) stopAllOutgoingTypingLoops() {
	tc.mu.Lock()
	states := make([]*outboundTypingState, 0, len(tc.outboundTyping))
	for key, state := range tc.outboundTyping {
		delete(tc.outboundTyping, key)
		states = append(states, state)
	}
	tc.mu.Unlock()
	for _, state := range states {
		if state != nil && state.cancel != nil {
			state.cancel()
		}
	}
}
