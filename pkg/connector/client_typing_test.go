package connector

import (
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
