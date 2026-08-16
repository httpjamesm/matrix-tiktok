package libtiktok

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
)

// ErrRateLimited is returned when TikTok answers 429. It carries how long the
// server asked us to wait, when it said, so a caller can honour that rather
// than retrying on its own schedule. Retrying through a throttle is how a
// throttle becomes a block.
type ErrRateLimited struct {
	RetryAfter time.Duration
}

func (e *ErrRateLimited) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limited by TikTok, retry after %s", e.RetryAfter)
	}
	return "rate limited by TikTok"
}

// IsRateLimited reports whether err is, or wraps, a 429 from TikTok.
func IsRateLimited(err error) bool {
	var rl *ErrRateLimited
	return errors.As(err, &rl)
}

// ErrAuthRejected means the session is genuinely no longer valid and the user
// has to log in again. Reserved for 401, and for a page that loads fine but
// carries no signed-in user — the two cases where "your session is dead" is
// actually established.
type ErrAuthRejected struct {
	// StatusCode is the HTTP status, or 0 when the signal was a logged-out
	// page rather than a status.
	StatusCode int
}

func (e *ErrAuthRejected) Error() string {
	if e.StatusCode == 0 {
		return "TikTok served a logged-out page: the session is no longer valid"
	}
	return fmt.Sprintf("TikTok rejected the session (HTTP %d)", e.StatusCode)
}

// ErrAccessRestricted is a 403: the server refused this request. That is not
// the same as the session being dead.
//
// 403 used to be reported as bad credentials, which told the customer to
// reconnect their account. A 403 can equally be a geographic restriction, a WAF
// challenge, a temporarily limited endpoint or an action the account is not
// allowed to take — none of which a new login fixes, and all of which cost the
// customer a working session if they follow the instruction and the relogin
// then fails. Keep the cookies, stop retrying, and say what is actually known.
type ErrAccessRestricted struct {
	StatusCode int
}

func (e *ErrAccessRestricted) Error() string {
	return fmt.Sprintf("TikTok refused the request (HTTP %d)", e.StatusCode)
}

// IsAccessRestricted reports whether err is, or wraps, a refusal that is not a
// credential problem.
func IsAccessRestricted(err error) bool {
	var ar *ErrAccessRestricted
	return errors.As(err, &ar)
}

// IsAuthRejected reports whether err is, or wraps, a session TikTok has
// established is no longer valid.
//
// It also matches the older free-text 401 message. Callers used to detect this
// by string-matching the error, which silently stopped working the moment the
// wording changed — the failure mode being that a dead session reports as a
// temporary glitch and the bridge keeps retrying it forever.
func IsAuthRejected(err error) bool {
	var ar *ErrAuthRejected
	if errors.As(err, &ar) {
		return true
	}
	msg := err.Error()
	for _, legacy := range []string{"unexpected status 401", "returned HTTP 401"} {
		if strings.Contains(msg, legacy) {
			return true
		}
	}
	return false
}

// rateGate holds an account-wide pause.
//
// Handling a throttle where it was observed is not enough: backfill would stop
// paginating one conversation and immediately start on the next, and sending,
// typing and read receipts carried on regardless. TikTok throttles the account,
// not the call site, so the pause has to be account-wide too. Any 429 extends
// it and every request waits it out.
type rateGate struct {
	mu    sync.Mutex
	until time.Time
}

// block extends the pause to at least d from now. Never shortens an existing
// one — two throttles arriving together should not cancel each other out.
func (g *rateGate) block(d time.Duration) {
	if d <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if deadline := time.Now().Add(d); deadline.After(g.until) {
		g.until = deadline
	}
}

// wait blocks until the pause has elapsed, or ctx ends. Returns ctx.Err() if
// the caller was cancelled while waiting, so a shutdown is not held up by a
// throttle someone else triggered.
func (g *rateGate) wait(ctx context.Context) error {
	g.mu.Lock()
	remaining := time.Until(g.until)
	g.mu.Unlock()
	if remaining <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(remaining):
		return nil
	}
}

// blockedFor reports the remaining pause, for logging and tests.
func (g *rateGate) blockedFor() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return max(time.Until(g.until), 0)
}

// checkResponse turns a non-2xx response into an error, and does the two things
// every call site was doing without: it distinguishes a throttle from a
// failure, and it forgets the cached page when the server rejects our
// credentials for it, so the next attempt refetches instead of failing forever
// on a stale value.
func (c *Client) checkResponse(what string, resp *resty.Response) error {
	if !resp.IsError() {
		return nil
	}
	code := resp.StatusCode()
	retryAfter := parseRetryAfter(resp.Header().Get("Retry-After"))
	switch {
	case code == http.StatusTooManyRequests:
		c.gate.block(ThrottleBackoff(&ErrRateLimited{RetryAfter: retryAfter}))
		return &ErrRateLimited{RetryAfter: retryAfter}
	case code == http.StatusServiceUnavailable && retryAfter > 0:
		// 503 with a Retry-After is a cooldown, not a crash. Treating it as a
		// generic failure means retrying straight through a window the server
		// explicitly asked us to sit out.
		c.gate.block(ThrottleBackoff(&ErrRateLimited{RetryAfter: retryAfter}))
		return &ErrRateLimited{RetryAfter: retryAfter}
	case code == http.StatusUnauthorized:
		// A rejected credential is the one case where the page we cached may
		// be the thing that is wrong.
		c.invalidateUniversalData()
		return &ErrAuthRejected{StatusCode: code}
	case code == http.StatusForbidden:
		// Refused, but not established as a dead session. Drop the cached page
		// in case it is stale, and stop - without telling anyone to relogin.
		c.invalidateUniversalData()
		return &ErrAccessRestricted{StatusCode: code}
	}
	return fmt.Errorf("%s API returned %d: %s", what, code, resp.String())
}

// checkHTTPResponse is checkResponse for call sites that fetch media rather
// than call the API: same throttle detection, no cache invalidation, and a
// message naming the URL.
func (c *Client) checkHTTPResponse(what, url string, resp *resty.Response) error {
	if !resp.IsError() {
		return nil
	}
	code := resp.StatusCode()
	retryAfter := parseRetryAfter(resp.Header().Get("Retry-After"))
	if code == http.StatusTooManyRequests || (code == http.StatusServiceUnavailable && retryAfter > 0) {
		c.gate.block(ThrottleBackoff(&ErrRateLimited{RetryAfter: retryAfter}))
		return &ErrRateLimited{RetryAfter: retryAfter}
	}
	if code == http.StatusUnauthorized {
		c.invalidateUniversalData()
		return &ErrAuthRejected{StatusCode: code}
	}
	if code == http.StatusForbidden {
		c.invalidateUniversalData()
		return &ErrAccessRestricted{StatusCode: code}
	}
	return fmt.Errorf("%s %s returned HTTP %d", what, url, code)
}

// parseRetryAfter reads a Retry-After header as delta-seconds or an HTTP-date.
// Anything unparsable reads as zero, so a bad header is never worse than none.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if until := time.Until(at); until > 0 {
			return until
		}
	}
	return 0
}
