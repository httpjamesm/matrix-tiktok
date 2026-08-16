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
)

// Stress tests drive the client against a deliberately hostile server rather
// than TikTok. Hammering the real thing to find its limits is how accounts get
// banned; a server that returns whatever we want, whenever we want, finds the
// same bugs and costs nothing.
//
// Run with -race. Several of these assert on request *counts* and *timing*,
// because the ban risk is a traffic pattern, not a return value.

// hostile is a test server that can be told to misbehave.
type hostile struct {
	*httptest.Server
	requests atomic.Int64
	// times records when each request arrived, for pacing assertions.
	mu    sync.Mutex
	times []time.Time
}

func newHostile(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, n int64)) *hostile {
	t.Helper()
	h := &hostile{}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := h.requests.Add(1)
		h.mu.Lock()
		h.times = append(h.times, time.Now())
		h.mu.Unlock()
		handler(w, r, n)
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *hostile) count() int64 { return h.requests.Load() }

func (h *hostile) gaps() []time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []time.Duration
	for i := 1; i < len(h.times); i++ {
		out = append(out, h.times[i].Sub(h.times[i-1]))
	}
	return out
}

// clientAgainst points a real Client at the hostile server.
func clientAgainst(h *hostile) *Client {
	c := NewClient("sessionid=test", "")
	c.r.SetBaseURL(h.URL)
	c.rIA.SetBaseURL(h.URL)
	return c
}

// --- 1. Throttle storm on the page load -------------------------------------

// A 429 on /messages must not be retried like a transient failure. Retrying
// into a throttle is the single most reliable way to escalate one.
func TestPageLoad_DoesNotRetryInto429(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c := clientAgainst(h)

	start := time.Now()
	_, err := c.fetchMessagesUniversalData()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a 429-ing server")
	}
	if got := h.count(); got != 1 {
		t.Errorf("sent %d requests to a server that said 429 Retry-After 120; want 1 (a throttle is not a retryable failure)", got)
	}
	if !IsRateLimited(err) {
		t.Errorf("429 surfaced as %v; want a typed rate-limit error so callers can honour Retry-After", err)
	}
	t.Logf("requests=%d elapsed=%v err=%v", h.count(), elapsed.Round(time.Millisecond), err)
}

// A 500, by contrast, is a genuine transient failure and should be retried.
func TestPageLoad_StillRetries500(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := clientAgainst(h)
	_, _ = c.fetchMessagesUniversalData()
	if got := h.count(); got != messagesPageAttempts {
		t.Errorf("sent %d requests on repeated 500s; want %d (retrying a real failure is correct)", got, messagesPageAttempts)
	}
}

// --- 2. Cancellation --------------------------------------------------------

// The page load sleeps up to ~5s between attempts. If it cannot be cancelled,
// shutdown blocks and a reconnect can overlap the previous attempt.
func TestPageLoad_RespectsContextCancellation(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		w.WriteHeader(http.StatusInternalServerError) // force the retry sleep
	})
	c := clientAgainst(h)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.fetchMessagesUniversalDataCtx(ctx)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("page load ignored context cancellation and kept going (%d requests so far)", h.count())
	}
}

// --- 3. Rate limiting on the read paths -------------------------------------

// Downloads are 429-blind: a throttled media fetch looks like any other error,
// so the caller has nothing to back off on.
func TestDownload_SurfacesRateLimit(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c := clientAgainst(h)

	_, _, err := c.DownloadCover(context.Background(), h.URL+"/cover.jpg")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsRateLimited(err) {
		t.Errorf("throttled download surfaced as %v; want a typed rate-limit error", err)
	}
}

// --- 4. Garbage and truncation ----------------------------------------------

// A hostile or broken server must not be able to panic the bridge.
func TestGarbageResponsesDoNotPanic(t *testing.T) {
	bodies := []string{
		"",
		"not html at all",
		`<html><script>window.__UNIVERSAL_DATA_FOR_REHYDRATION__ = {truncated`,
		`<html><script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">{"broken":</script>`,
		`<html><script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">null</script>`,
		strings.Repeat("A", 1<<20),
		"\x00\x01\x02\xff\xfe",
	}
	for i, body := range bodies {
		t.Run(fmt.Sprintf("body%d", i), func(t *testing.T) {
			h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
				_, _ = w.Write([]byte(body))
			})
			c := clientAgainst(h)
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panicked on hostile body: %v", r)
				}
			}()
			_, _ = c.fetchMessagesUniversalData()
		})
	}
}

// --- 5. Concurrency ---------------------------------------------------------

// Many goroutines hitting the cached page load at once must produce one fetch,
// not a thundering herd, and must not race.
func TestUniversalDataCache_NoThunderingHerd(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		time.Sleep(50 * time.Millisecond) // make the window wide enough to collide
		_, _ = w.Write([]byte(`<html><script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">{"ok":1}</script></html>`))
	})
	c := clientAgainst(h)

	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.getMessagesUniversalData()
		}()
	}
	wg.Wait()

	if got := h.count(); got > 1 {
		t.Errorf("25 concurrent callers caused %d page loads; want 1 (a burst of identical requests is exactly the pattern that gets flagged)", got)
	}
}

// --- 6. Auth rejection ------------------------------------------------------

// A 401 must invalidate the cached page data, or a stale token keeps failing
// forever and the bridge retries with credentials the server already rejected.
func TestAuthRejectionInvalidatesCache(t *testing.T) {
	var mode atomic.Int32 // 0 = ok, 1 = reject
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		if mode.Load() == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`<html><script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">{"ok":1}</script></html>`))
	})
	c := clientAgainst(h)

	if _, err := c.getMessagesUniversalData(); err != nil {
		t.Fatalf("warm-up load failed: %v", err)
	}
	before := h.count()

	// Cached: no new request.
	if _, err := c.getMessagesUniversalData(); err != nil {
		t.Fatalf("cached load failed: %v", err)
	}
	if h.count() != before {
		t.Errorf("cache did not serve the second call")
	}

	// Server starts rejecting the credentials the cache was built with.
	mode.Store(1)
	c.invalidateUniversalData()
	if _, err := c.getMessagesUniversalData(); err == nil {
		t.Error("expected an error once the server rejects the session")
	}
}
