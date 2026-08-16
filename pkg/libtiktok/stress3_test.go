package libtiktok

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Round 3: scenarios that specifically get an account flagged, rather than
// scenarios that break the bridge. The bridge working perfectly while looking
// like a bot is the failure mode that matters here.

// --- 13. Browser fingerprint consistency ------------------------------------

// The authenticated clients announce Chrome. A real Chrome always sends the
// Sec-Fetch-* metadata headers with them; a request claiming Chrome that omits
// them is trivially separable from a browser. This library already knows that —
// it is the documented reason the media scraper uses a curl User-Agent instead.
// The client carrying the session should not have the mismatch the scraper
// deliberately avoids.
func TestAuthenticatedClientFingerprintIsConsistent(t *testing.T) {
	var got http.Header
	var mu sync.Mutex
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		mu.Lock()
		got = r.Header.Clone()
		mu.Unlock()
		_, _ = w.Write([]byte(`<html><script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">{"ok":1}</script></html>`))
	})
	c := clientAgainst(h)
	_, _ = c.fetchMessagesUniversalData()

	mu.Lock()
	defer mu.Unlock()
	ua := got.Get("User-Agent")
	if ua == "" {
		t.Fatal("no User-Agent sent")
	}
	// Only applies to a browser-claiming UA; a curl UA is consistent already.
	if !strings.Contains(ua, "Chrome") && !strings.Contains(ua, "Firefox") {
		t.Skipf("non-browser UA %q needs no Sec-Fetch metadata", ua)
	}
	for _, header := range []string{"Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest"} {
		if got.Get(header) == "" {
			t.Errorf("UA claims a browser (%q) but %s is missing; that combination is what TikTok's WAF challenges", ua, header)
		}
	}
}

// --- 14. Retrying a session the server already refused ----------------------

// A dead cookie must stop the bridge, not make it retry. Re-presenting a
// credential the server refused, over and over, is both pointless and one of
// the clearest automation signals there is.
func TestRejectedSessionIsNotRetried(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	c := clientAgainst(h)

	_, err := c.fetchMessagesUniversalData()
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := h.count(); got != 1 {
		t.Errorf("presented a refused session %d times; want 1", got)
	}
	if !IsAuthRejected(err) {
		t.Errorf("401 surfaced as %v; the bridge cannot tell the user to log in again unless this is typed", err)
	}
}

// The bridge must classify a rejected session as bad credentials. This used to
// be done by string-matching the error text, which broke silently when the
// wording changed: a dead session then reported as a temporary glitch and the
// bridge kept retrying it.
func TestAuthRejectionSurvivesRewording(t *testing.T) {
	for _, err := range []error{
		&ErrAuthRejected{StatusCode: 401},
		&ErrAuthRejected{StatusCode: 403},
	} {
		if !IsAuthRejected(err) {
			t.Errorf("%v not recognised as a rejected session", err)
		}
		if IsRateLimited(err) {
			t.Errorf("%v misclassified as a throttle", err)
		}
	}
}

// --- 15. Synchronised behaviour across logins -------------------------------

// Several accounts run through one bridge process. If they all refresh the
// cached page on the same fixed TTL, and they were started together (a restart,
// a deploy), their refreshes stay locked together forever: N identical requests
// from one IP at the same instant, repeating on a precise interval. Machines
// do that; people do not.
func TestCacheTTLIsJitteredAcrossLogins(t *testing.T) {
	const logins = 12
	ttls := make(map[time.Duration]int)
	for i := 0; i < logins; i++ {
		c := NewClient("sessionid=x", "")
		ttls[c.universalDataTTL()]++
	}
	if len(ttls) == 1 {
		for d := range ttls {
			t.Errorf("all %d logins share an identical %v refresh interval; started together they stay in lockstep forever", logins, d)
		}
	}
}

// --- 16. Recovery after a throttle ------------------------------------------

// Everything paused by one 429 must not resume at the same instant. A dozen
// operations that all wait exactly Retry-After and then fire together turn one
// throttle into a burst — which is what caused the throttle.
func TestThrottleRecoveryIsStaggered(t *testing.T) {
	const waiters = 10
	delays := make([]time.Duration, waiters)
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			delays[i] = ThrottleBackoff(&ErrRateLimited{RetryAfter: 5 * time.Second})
		}(i)
	}
	wg.Wait()

	seen := make(map[time.Duration]bool)
	for _, d := range delays {
		if d < 5*time.Second {
			t.Errorf("resumed after %v, sooner than the %v the server asked for", d, 5*time.Second)
		}
		seen[d] = true
	}
	if len(seen) == 1 {
		t.Errorf("all %d waiters resume at exactly the same moment; the retry burst recreates the throttle", waiters)
	}
}

// A throttle with no Retry-After still needs a sane, jittered wait rather than
// an immediate retry.
func TestThrottleBackoffWithoutHeader(t *testing.T) {
	d := ThrottleBackoff(&ErrRateLimited{})
	if d <= 0 {
		t.Errorf("no Retry-After produced a %v wait; an immediate retry into a throttle escalates it", d)
	}
	if d > 5*time.Minute {
		t.Errorf("no Retry-After produced a %v wait, long enough to look like a hang", d)
	}
}

// --- 17. Send pacing --------------------------------------------------------

// Nothing paces outgoing messages. A loop that sends to many people as fast as
// the API accepts them is precisely the pattern platforms ban for; WhatsApp
// names its equivalent ban code after it.
func TestOutgoingSendsArePaced(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		_, _ = w.Write([]byte(`{"status_code":0}`))
	})
	c := clientAgainst(h)

	const sends = 8
	start := time.Now()
	for i := 0; i < sends; i++ {
		c.PaceOutgoing(context.Background())
		_, _ = c.rIA.R().Post("/send")
	}
	elapsed := time.Since(start)

	gaps := h.gaps()
	var instant int
	for _, g := range gaps {
		if g < 50*time.Millisecond {
			instant++
		}
	}
	if instant > 1 {
		t.Errorf("%d of %d consecutive sends went out with under 50ms between them (total %v); a human cannot type that fast", instant, len(gaps), elapsed)
	}
}

// Pacing must not be defeatable by concurrency: ten goroutines sending at once
// must still be spaced, since they share one account.
func TestSendPacingIsGlobalNotPerGoroutine(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		_, _ = w.Write([]byte(`{"status_code":0}`))
	})
	c := clientAgainst(h)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.PaceOutgoing(context.Background())
			_, _ = c.rIA.R().Post("/send")
		}()
	}
	wg.Wait()

	var instant int
	for _, g := range h.gaps() {
		if g < 50*time.Millisecond {
			instant++
		}
	}
	if instant > 1 {
		t.Errorf("%d concurrent sends bypassed pacing; the limiter has to be per-account, not per-caller", instant)
	}
}

// Pacing must be interruptible, or shutdown waits for the whole queue.
func TestSendPacingRespectsContext(t *testing.T) {
	c := NewClient("sessionid=x", "")
	c.PaceOutgoing(context.Background()) // consume the initial allowance

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	c.PaceOutgoing(ctx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("pacing ignored a cancelled context and waited %v", elapsed)
	}
}

// --- 18. Duplicate sends -----------------------------------------------------

// A send that times out may well have been delivered. Retrying it blindly
// double-posts, which reads as spam to the recipient and to TikTok.
func TestAmbiguousSendIsNotBlindlyRetried(t *testing.T) {
	var delivered atomic.Int64
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		delivered.Add(1)
		time.Sleep(300 * time.Millisecond) // server receives it, reply is slow
		_, _ = w.Write([]byte(`{"status_code":0}`))
	})
	c := clientAgainst(h)
	c.rIA.SetTimeout(100 * time.Millisecond) // client gives up before the reply

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = c.rIA.R().SetContext(ctx).Post("/send")

	time.Sleep(500 * time.Millisecond)
	if got := delivered.Load(); got > 1 {
		t.Errorf("one send reached the server %d times; a timed-out send may already have been delivered", got)
	}
}

// --- 19. The session must still reach TikTok --------------------------------

// Scoping the cookie to TikTok hosts, and moving it off the client header,
// must not stop it being sent to TikTok — nor stop call sites that build
// request payloads from reading it back.
func TestSessionStillSentToTikTokItself(t *testing.T) {
	const session = "sessionid=REAL_SESSION; tt_csrf_token=xyz"
	c := NewClient(session, "")

	if c.sessionCookie() != session {
		t.Errorf("call sites read the raw cookie to build payloads; got %q", c.sessionCookie())
	}

	for _, host := range []string{
		"www.tiktok.com", "im-api-sg.tiktok.com", "TIKTOK.COM",
		"v16-webapp.tiktokcdn.com", "sub.tiktokv.com", "",
	} {
		if !isTikTokHost(host) {
			t.Errorf("isTikTokHost(%q) = false; the session would not reach TikTok", host)
		}
	}
	for _, host := range []string{
		"evil.com", "tiktok.com.evil.com", "nottiktok.com",
		"evil.com:443", "localhost",
	} {
		if isTikTokHost(host) {
			t.Errorf("isTikTokHost(%q) = true; the session would leak there", host)
		}
	}
}
