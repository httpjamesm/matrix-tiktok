package connector

import (
	"math/rand/v2"
	"testing"
	"time"
)

// reconnectDecision mirrors the branch wsLoop takes after a WebSocket closes.
// The loop itself needs a live client and a real dial, so the scheduling rule
// is factored out here — it is the part that can regress silently, and the part
// whose failure mode is an unthrottled redial loop against TikTok.
func reconnectDecision(connectedFor time.Duration, backoff time.Duration) (wait time.Duration, reset bool) {
	if connectedFor >= minStableConnection {
		return 0, true
	}
	return backoff + time.Duration((rand.Float64()*0.4-0.2)*float64(backoff)), false
}

// A connection the server accepts and drops immediately must be backed off.
// This used to reconnect with no delay at all while logging a retry_in it never
// honoured, so a server soft-refusing a session got redialled as fast as TCP
// and TLS allowed, forever, without the backoff ever escalating.
func TestShortLivedConnectionIsBackedOff(t *testing.T) {
	backoff := initialReconnectBackoff
	for _, connectedFor := range []time.Duration{
		0,
		time.Second,
		30 * time.Second,
		minStableConnection - time.Second,
	} {
		wait, reset := reconnectDecision(connectedFor, backoff)
		if reset {
			t.Errorf("a connection lasting %v was treated as stable", connectedFor)
		}
		if wait <= 0 {
			t.Errorf("a connection lasting %v reconnected with no delay", connectedFor)
		}
	}
}

// A connection that held earns an immediate reconnect: a real network blip
// should not leave someone waiting minutes for their messages.
func TestStableConnectionReconnectsImmediately(t *testing.T) {
	wait, reset := reconnectDecision(minStableConnection+time.Second, 80*time.Second)
	if !reset {
		t.Error("a stable connection did not reset the backoff")
	}
	if wait != 0 {
		t.Errorf("a stable connection waited %v before reconnecting", wait)
	}
}

// Repeated short connections must escalate rather than sit at the first step.
// The old code reset the backoff on every successful dial, so a connect-drop
// cycle could never climb out of its initial interval.
func TestRepeatedShortConnectionsEscalate(t *testing.T) {
	const maxBackoff = 5 * time.Minute
	backoff := initialReconnectBackoff
	var first, last time.Duration
	for i := 0; i < 6; i++ {
		wait, reset := reconnectDecision(time.Second, backoff)
		if reset {
			t.Fatal("short connection reported stable")
		}
		if i == 0 {
			first = wait
		}
		last = wait
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
	if last <= first {
		t.Errorf("backoff did not escalate across repeated short connections: %v then %v", first, last)
	}
}

// The waits must not be identical across logins, or every account on the host
// retries on the same staircase.
func TestReconnectWaitIsJittered(t *testing.T) {
	seen := map[time.Duration]bool{}
	for i := 0; i < 20; i++ {
		wait, _ := reconnectDecision(time.Second, initialReconnectBackoff)
		seen[wait] = true
	}
	if len(seen) == 1 {
		t.Error("every login would reconnect at exactly the same moment")
	}
}
