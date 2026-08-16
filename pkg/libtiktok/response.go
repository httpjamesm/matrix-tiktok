package libtiktok

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

// ErrAuthRejected is returned when TikTok rejects the session outright (401 or
// 403). The bridge must surface this as bad credentials and stop, rather than
// retrying: repeatedly presenting a credential the server has already refused
// is both useless and one of the clearer automation signals.
type ErrAuthRejected struct {
	StatusCode int
}

func (e *ErrAuthRejected) Error() string {
	return fmt.Sprintf("TikTok rejected the session (HTTP %d)", e.StatusCode)
}

// IsAuthRejected reports whether err is, or wraps, a rejected session.
//
// It also matches the older free-text messages. Callers used to detect this by
// string-matching the error, which silently stopped working the moment the
// wording changed — the failure mode being that a dead session reports as a
// temporary glitch and the bridge keeps retrying it forever.
func IsAuthRejected(err error) bool {
	var ar *ErrAuthRejected
	if errors.As(err, &ar) {
		return true
	}
	msg := err.Error()
	for _, legacy := range []string{"unexpected status 401", "unexpected status 403", "returned HTTP 401", "returned HTTP 403"} {
		if strings.Contains(msg, legacy) {
			return true
		}
	}
	return false
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
		return &ErrRateLimited{RetryAfter: retryAfter}
	case code == http.StatusServiceUnavailable && retryAfter > 0:
		// 503 with a Retry-After is a cooldown, not a crash. Treating it as a
		// generic failure means retrying straight through a window the server
		// explicitly asked us to sit out.
		return &ErrRateLimited{RetryAfter: retryAfter}
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		// A rejected credential is the one case where the page we cached may
		// be the thing that is wrong.
		c.invalidateUniversalData()
		return &ErrAuthRejected{StatusCode: code}
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
		return &ErrRateLimited{RetryAfter: retryAfter}
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		c.invalidateUniversalData()
		return &ErrAuthRejected{StatusCode: code}
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
