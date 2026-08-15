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
	switch code {
	case http.StatusTooManyRequests:
		return &ErrRateLimited{RetryAfter: parseRetryAfter(resp.Header().Get("Retry-After"))}
	case http.StatusUnauthorized, http.StatusForbidden:
		// A rejected credential is the one case where the page we cached may
		// be the thing that is wrong.
		c.invalidateUniversalData()
	}
	return fmt.Errorf("%s API returned %d: %s", what, code, resp.String())
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
