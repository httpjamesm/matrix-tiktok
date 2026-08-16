package libtiktok

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
)

// Round 2: the failure modes a real adversary or a bad network produces, rather
// than the ones a well-behaved server does. Everything here runs against a
// local server.

// --- 7. Credential leakage --------------------------------------------------

// DownloadPrivateImage and DownloadPrivateVideo take a URL that arrives inside
// a message — i.e. chosen by whoever is talking to the user — and fetch it with
// the client that carries the session cookie. If that URL points anywhere but
// TikTok, the session goes with it.
func TestPrivateDownload_DoesNotLeakSessionCookie(t *testing.T) {
	var gotCookie atomic.Value
	gotCookie.Store("")
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie.Store(r.Header.Get("Cookie"))
		_, _ = w.Write([]byte("gotcha"))
	}))
	defer evil.Close()

	c := NewClient("sessionid=SUPER_SECRET_SESSION; tt_csrf_token=abc", "")

	// A URL an attacker put in a DM.
	_, _, _ = c.DownloadPrivateVideo(context.Background(), evil.URL+"/attacker.mp4")

	if leaked := gotCookie.Load().(string); strings.Contains(leaked, "SUPER_SECRET_SESSION") {
		t.Errorf("session cookie was sent to a non-TikTok host chosen by the message sender: %q", leaked)
	}
}

// The same hole via redirect. Note both test servers listen on 127.0.0.1, which
// Go's redirect logic treats as the same host, so it will not strip the header
// here — the point of this test is the destination the bytes actually reach,
// not Go's same-host heuristic.
func TestPrivateDownload_CookieStrippedOnCrossHostRedirect(t *testing.T) {
	var gotCookie atomic.Value
	gotCookie.Store("")
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie.Store(r.Header.Get("Cookie"))
		_, _ = w.Write([]byte("ok"))
	}))
	defer final.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/next", http.StatusFound)
	}))
	defer redirector.Close()

	c := NewClient("sessionid=SUPER_SECRET_SESSION", "")
	_, _, _ = c.DownloadPrivateVideo(context.Background(), redirector.URL+"/start")

	if leaked := gotCookie.Load().(string); strings.Contains(leaked, "SUPER_SECRET_SESSION") {
		t.Errorf("session cookie survived a cross-host redirect: %q", leaked)
	}
}

// --- 8. Hangs and slow servers ----------------------------------------------

// No timeout is configured on either HTTP client. A server that accepts the
// connection and then says nothing holds the goroutine forever.
func TestHungServer_DoesNotBlockForever(t *testing.T) {
	release := make(chan struct{})
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		<-release // never respond until the test says so
	})
	defer close(release)

	c := clientAgainst(h)
	done := make(chan error, 1)
	go func() {
		_, _, err := c.DownloadCover(context.Background(), h.URL+"/hang.jpg")
		done <- err
	}()

	select {
	case <-done:
	case <-time.After(35 * time.Second):
		t.Fatal("a hung server blocked the request indefinitely; no client timeout is configured")
	}
}

// Slow-loris: a trickle of bytes, forever. Without a timeout this never ends.
func TestSlowTrickle_DoesNotBlockForever(t *testing.T) {
	stop := make(chan struct{})
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = w.Write([]byte("A"))
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	defer close(stop)

	// A trickle delivers headers immediately, so ResponseHeaderTimeout does not
	// apply; only the overall request timeout ends it. Shorten that here rather
	// than making the suite wait the production value.
	c := clientAgainst(h)
	const testTimeout = 2 * time.Second
	c.scraper.SetTimeout(testTimeout)

	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = c.DownloadCover(context.Background(), h.URL+"/slow.jpg")
	}()

	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > testTimeout*3 {
			t.Errorf("trickle ran %v against a %v timeout", elapsed, testTimeout)
		}
	case <-time.After(testTimeout * 3):
		t.Fatal("a slow trickle held the request open past the configured timeout")
	}
}

// The production timeout must actually be set, since the test above overrides
// it. Without this, shortening the timeout in a test would hide its absence.
func TestClientsHaveTimeouts(t *testing.T) {
	c := NewClient("sessionid=x", "")
	for name, rc := range map[string]*resty.Client{"www": c.r, "im-api": c.rIA, "scraper": c.scraper} {
		if got := rc.GetClient().Timeout; got <= 0 {
			t.Errorf("%s client has no timeout; a hung server holds the goroutine forever", name)
		} else if got > 5*time.Minute {
			t.Errorf("%s client timeout is %v, too long to be a bound", name, got)
		}
	}
}

// --- 9. Memory ---------------------------------------------------------------

// Nothing caps the response body. A hostile or broken CDN response is read
// entirely into memory, and the bridge is a long-lived process.
func TestOversizedBody_IsCapped(t *testing.T) {
	const huge = 256 << 20 // 256 MiB
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		w.Header().Set("Content-Type", "image/jpeg")
		chunk := make([]byte, 1<<20)
		for i := 0; i < huge>>20; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	c := clientAgainst(h)

	body, _, err := c.DownloadCover(context.Background(), h.URL+"/huge.jpg")
	if err == nil && len(body) >= huge {
		t.Errorf("read %d bytes into memory with no cap; a single hostile response can exhaust the process", len(body))
	}
}

// --- 10. Retry-After edge values ---------------------------------------------

func TestRetryAfterEdgeValues(t *testing.T) {
	cases := []struct {
		header string
		want   string // description of acceptable handling
	}{
		{"0", "zero"},
		{"-5", "negative"},
		{"not-a-number", "garbage"},
		{"86400", "a full day"},
		{"999999999999999999999", "overflow"},
		{"", "absent"},
		{"Thu, 01 Jan 1970 00:00:00 GMT", "past date"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			d := parseRetryAfter(tc.header)
			if d < 0 {
				t.Errorf("parseRetryAfter(%q) = %v; a negative delay would make a caller busy-loop", tc.header, d)
			}
			if d > 24*time.Hour {
				t.Errorf("parseRetryAfter(%q) = %v; an unbounded delay stalls the bridge indefinitely", tc.header, d)
			}
		})
	}
}

// 503 with Retry-After is also a throttle. Treating it as a plain server error
// means retrying straight through a documented cooldown.
func TestServiceUnavailableWithRetryAfterIsThrottle(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c := clientAgainst(h)
	_, _, err := c.DownloadCover(context.Background(), h.URL+"/x.jpg")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsRateLimited(err) {
		t.Errorf("503 + Retry-After surfaced as %v; want it treated as a throttle", err)
	}
}

// --- 11. Concurrency under hostility -----------------------------------------

// Invalidation racing with reads must not corrupt the cache or double-fetch.
// Run under -race.
func TestCacheInvalidationRace(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		_, _ = w.Write([]byte(`<html><script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">{"n":1}</script></html>`))
	})
	c := clientAgainst(h)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				c.invalidateUniversalData()
				return
			}
			_, _ = c.getMessagesUniversalData()
		}(i)
	}
	wg.Wait()
	t.Logf("page loads under 50 racing callers: %d", h.count())
}

// A burst of concurrent downloads must not open an unbounded number of
// simultaneous connections to the same host.
func TestConcurrentDownloads_AreBounded(t *testing.T) {
	var inFlight, peak atomic.Int64
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		cur := inFlight.Add(1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		inFlight.Add(-1)
		_, _ = w.Write([]byte("img"))
	})
	c := clientAgainst(h)

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, _ = c.DownloadCover(context.Background(), fmt.Sprintf("%s/i%d.jpg", h.URL, i))
		}(i)
	}
	wg.Wait()
	t.Logf("peak simultaneous connections from 40 concurrent downloads: %d", peak.Load())
	if peak.Load() > 20 {
		t.Errorf("opened %d simultaneous connections to one host; a real browser does not", peak.Load())
	}
}

// --- 12. Connection dropped mid-body -----------------------------------------

func TestTruncatedResponse_IsAnError(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("short"))
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close() // drop mid-body
			}
		}
	})
	c := clientAgainst(h)
	body, _, err := c.DownloadCover(context.Background(), h.URL+"/trunc.jpg")
	if err == nil {
		t.Errorf("a truncated body was accepted as a complete download (%d bytes); corrupt media would be bridged as real", len(body))
	}
}
