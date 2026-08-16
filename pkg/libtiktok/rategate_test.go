package libtiktok

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// A throttle on one call must pause the whole account, not just that call site.
// Backfill used to stop paginating one conversation and start straight on the
// next; sending, typing and read receipts carried on regardless.
func TestThrottleOnOneCallPausesEverything(t *testing.T) {
	var throttleOnce atomic.Bool
	var afterThrottle atomic.Int64
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		if throttleOnce.CompareAndSwap(false, true) {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		afterThrottle.Add(1)
		_, _ = w.Write([]byte("ok"))
	})
	c := clientAgainst(h)

	// Trip the gate.
	if _, _, err := c.DownloadCover(context.Background(), h.URL+"/a.jpg"); !IsRateLimited(err) {
		t.Fatalf("expected a rate-limit error, got %v", err)
	}
	if blocked := c.gate.blockedFor(); blocked <= 0 {
		t.Fatal("a 429 did not arm the account-wide pause")
	}

	// A different call, on a different client, must wait it out.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, _ = c.DownloadCover(ctx, h.URL+"/b.jpg")
	if afterThrottle.Load() > 0 {
		t.Errorf("a request went out %v after the throttle, inside the pause", time.Since(start))
	}
}

// The pause extends, never shortens: two throttles arriving together must not
// cancel each other out.
func TestGateNeverShortens(t *testing.T) {
	var g rateGate
	g.block(10 * time.Second)
	first := g.blockedFor()
	g.block(time.Second)
	if g.blockedFor() < first-100*time.Millisecond {
		t.Errorf("a shorter throttle shortened the pause: %v then %v", first, g.blockedFor())
	}
}

// Shutdown must not be held up by someone else's throttle.
func TestGateReleasesOnContextCancel(t *testing.T) {
	var g rateGate
	g.block(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := g.wait(ctx); err == nil {
		t.Error("wait returned nil for a cancelled context")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("cancelled wait still blocked for %v", elapsed)
	}
}
