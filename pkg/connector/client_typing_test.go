package connector

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/httpjamesm/matrix-tiktok/pkg/libtiktok"
)

func newTypingTestClient(t *testing.T, timeout time.Duration) (*TikTokClient, chan *simplevent.Typing) {
	t.Helper()

	events := make(chan *simplevent.Typing, 8)
	tc := &TikTokClient{
		meta:   &UserLoginMetadata{UserID: syntheticSelfUserID},
		typing: make(map[typingStateKey]*typingTimerState),

		typingTimeout: timeout,
		userLogin: &bridgev2.UserLogin{
			UserLogin: &database.UserLogin{
				ID: networkid.UserLoginID("login"),
			},
			Log: zerolog.Nop(),
		},
		queueRemoteEventForTest: func(evt bridgev2.RemoteEvent) {
			typingEvt, ok := evt.(*simplevent.Typing)
			if !ok {
				t.Fatalf("unexpected remote event type %T", evt)
			}
			events <- typingEvt
		},
	}
	t.Cleanup(tc.stopAllTypingTimers)
	return tc, events
}

func waitForTypingEvent(t *testing.T, ch <-chan *simplevent.Typing, timeout time.Duration) *simplevent.Typing {
	t.Helper()
	select {
	case evt := <-ch:
		return evt
	case <-time.After(timeout):
		t.Fatal("timed out waiting for typing event")
		return nil
	}
}

func TestDispatchWSTypingRefreshesTimerAndStopsAfterInactivity(t *testing.T) {
	timeout := 40 * time.Millisecond
	tc, events := newTypingTestClient(t, timeout)
	startMs := uint64(1776740771718)
	ti := &libtiktok.WSTypingIndicator{
		ConversationID:       syntheticDMConvID,
		ConversationSourceID: syntheticConvSourceID,
		SenderUserID:         syntheticOtherUserID,
		CreateTimeMs:         startMs,
	}

	tc.dispatchWSTyping(ti)
	first := waitForTypingEvent(t, events, time.Second)
	if first.Timeout != timeout {
		t.Fatalf("first timeout = %s want %s", first.Timeout, timeout)
	}
	if first.PortalKey.ID != makePortalID(syntheticDMConvID) {
		t.Fatalf("portal id = %q want %q", first.PortalKey.ID, makePortalID(syntheticDMConvID))
	}
	if first.PortalKey.Receiver != networkid.UserLoginID("login") {
		t.Fatalf("portal receiver = %q", first.PortalKey.Receiver)
	}
	if first.Sender.Sender != makeUserID(syntheticOtherUserID) {
		t.Fatalf("sender = %q want %q", first.Sender.Sender, makeUserID(syntheticOtherUserID))
	}
	if first.CreatePortal {
		t.Fatal("CreatePortal = true, want false")
	}
	if !first.Timestamp.Equal(time.UnixMilli(int64(startMs))) {
		t.Fatalf("timestamp = %s want %s", first.Timestamp, time.UnixMilli(int64(startMs)))
	}
	if first.Type != bridgev2.TypingTypeText {
		t.Fatalf("typing type = %v want text", first.Type)
	}

	time.Sleep(timeout / 2)

	nextMs := startMs + 1
	tc.dispatchWSTyping(&libtiktok.WSTypingIndicator{
		ConversationID:       syntheticDMConvID,
		ConversationSourceID: syntheticConvSourceID,
		SenderUserID:         syntheticOtherUserID,
		CreateTimeMs:         nextMs,
	})
	second := waitForTypingEvent(t, events, time.Second)
	if second.Timeout != timeout {
		t.Fatalf("second timeout = %s want %s", second.Timeout, timeout)
	}
	if !second.Timestamp.Equal(time.UnixMilli(int64(nextMs))) {
		t.Fatalf("second timestamp = %s want %s", second.Timestamp, time.UnixMilli(int64(nextMs)))
	}

	select {
	case evt := <-events:
		t.Fatalf("received premature typing stop event: timeout=%s", evt.Timeout)
	case <-time.After(timeout/2 + 5*time.Millisecond):
	}

	stop := waitForTypingEvent(t, events, time.Second)
	if stop.Timeout != 0 {
		t.Fatalf("stop timeout = %s want 0", stop.Timeout)
	}
	if stop.Sender.Sender != makeUserID(syntheticOtherUserID) {
		t.Fatalf("stop sender = %q want %q", stop.Sender.Sender, makeUserID(syntheticOtherUserID))
	}
}

func TestDispatchWSTypingIgnoresSelfHeartbeats(t *testing.T) {
	tc, events := newTypingTestClient(t, 20*time.Millisecond)
	tc.dispatchWSTyping(&libtiktok.WSTypingIndicator{
		ConversationID:       syntheticDMConvID,
		ConversationSourceID: syntheticConvSourceID,
		SenderUserID:         syntheticSelfUserID,
		CreateTimeMs:         1776740771718,
	})

	select {
	case evt := <-events:
		t.Fatalf("unexpected typing event: %+v", evt)
	case <-time.After(50 * time.Millisecond):
	}

	tc.mu.Lock()
	defer tc.mu.Unlock()
	if len(tc.typing) != 0 {
		t.Fatalf("typing timers = %d want 0", len(tc.typing))
	}
}

func newOutboundTypingTestPortal() *bridgev2.Portal {
	return &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey: networkid.PortalKey{
				ID: makePortalID(syntheticDMConvID),
			},
			Metadata: &PortalMetadata{
				ConversationID:   syntheticDMConvID,
				SourceID:         syntheticConvSourceID,
				ConversationType: 1,
			},
		},
	}
}

func waitForTypingSend(t *testing.T, ch <-chan libtiktok.SendTypingParams, timeout time.Duration) libtiktok.SendTypingParams {
	t.Helper()
	select {
	case params := <-ch:
		return params
	case <-time.After(timeout):
		t.Fatal("timed out waiting for outgoing typing heartbeat")
		return libtiktok.SendTypingParams{}
	}
}

func TestHandleMatrixTypingStartsSingleLoopAndStops(t *testing.T) {
	sends := make(chan libtiktok.SendTypingParams, 8)
	tc := &TikTokClient{
		meta:                   &UserLoginMetadata{UserID: syntheticSelfUserID},
		outboundTyping:         make(map[string]*outboundTypingState),
		outboundTypingInterval: 25 * time.Millisecond,
		sendTypingForTest: func(_ context.Context, params libtiktok.SendTypingParams) error {
			sends <- params
			return nil
		},
	}
	t.Cleanup(tc.stopAllOutgoingTypingLoops)

	portal := newOutboundTypingTestPortal()
	ctx := context.Background()

	if err := tc.HandleMatrixTyping(ctx, &bridgev2.MatrixTyping{
		Portal:   portal,
		IsTyping: true,
		Type:     bridgev2.TypingTypeText,
	}); err != nil {
		t.Fatalf("HandleMatrixTyping start: %v", err)
	}

	first := waitForTypingSend(t, sends, time.Second)
	if first.ConvID != syntheticDMConvID {
		t.Fatalf("first conv_id = %q want %q", first.ConvID, syntheticDMConvID)
	}
	if first.ConvSourceID != syntheticConvSourceID {
		t.Fatalf("first source_id = %d want %d", first.ConvSourceID, syntheticConvSourceID)
	}

	if err := tc.HandleMatrixTyping(ctx, &bridgev2.MatrixTyping{
		Portal:   portal,
		IsTyping: true,
		Type:     bridgev2.TypingTypeText,
	}); err != nil {
		t.Fatalf("HandleMatrixTyping duplicate start: %v", err)
	}

	select {
	case extra := <-sends:
		t.Fatalf("unexpected immediate extra typing heartbeat after duplicate start: %+v", extra)
	case <-time.After(10 * time.Millisecond):
	}

	waitForTypingSend(t, sends, time.Second)

	if err := tc.HandleMatrixTyping(ctx, &bridgev2.MatrixTyping{
		Portal:   portal,
		IsTyping: false,
		Type:     bridgev2.TypingTypeText,
	}); err != nil {
		t.Fatalf("HandleMatrixTyping stop: %v", err)
	}

	select {
	case extra := <-sends:
		t.Fatalf("unexpected typing heartbeat after stop: %+v", extra)
	case <-time.After(40 * time.Millisecond):
	}

	tc.mu.Lock()
	defer tc.mu.Unlock()
	if len(tc.outboundTyping) != 0 {
		t.Fatalf("outbound typing loops = %d want 0", len(tc.outboundTyping))
	}
}

func TestDisconnectStopsOutgoingTypingLoops(t *testing.T) {
	sends := make(chan libtiktok.SendTypingParams, 8)
	tc := &TikTokClient{
		meta:                   &UserLoginMetadata{UserID: syntheticSelfUserID},
		outboundTyping:         make(map[string]*outboundTypingState),
		outboundTypingInterval: 25 * time.Millisecond,
		sendTypingForTest: func(_ context.Context, params libtiktok.SendTypingParams) error {
			sends <- params
			return nil
		},
	}

	if err := tc.HandleMatrixTyping(context.Background(), &bridgev2.MatrixTyping{
		Portal:   newOutboundTypingTestPortal(),
		IsTyping: true,
		Type:     bridgev2.TypingTypeText,
	}); err != nil {
		t.Fatalf("HandleMatrixTyping start: %v", err)
	}

	waitForTypingSend(t, sends, time.Second)
	tc.Disconnect()

	select {
	case extra := <-sends:
		t.Fatalf("unexpected typing heartbeat after disconnect: %+v", extra)
	case <-time.After(40 * time.Millisecond):
	}

	tc.mu.Lock()
	defer tc.mu.Unlock()
	if len(tc.outboundTyping) != 0 {
		t.Fatalf("outbound typing loops = %d want 0", len(tc.outboundTyping))
	}
}
