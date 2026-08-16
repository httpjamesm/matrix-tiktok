package libtiktok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"golang.org/x/net/html"
)

const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"

// secCHUA must stay consistent with the Chrome major version in
// DefaultUserAgent. A UA claiming one version while the client hints claim
// another is a stronger signal than either alone.
const secCHUA = `"Chromium";v="146", "Google Chrome";v="146", "Not?A_Brand";v="24"`

type Client struct {
	// r is the client for www.tiktok.com
	r *resty.Client
	// rIA is the client for the IM API specifically
	rIA *resty.Client
	// scraper fetches public pages and media. Shared rather than built per
	// call: a fresh client each time means a fresh connection pool each time,
	// so a burst of downloads opened a connection per item and none of them
	// inherited the proxy or the limits below.
	scraper *resty.Client

	// The parsed /messages page, kept for a while. Every action - sending,
	// reacting, marking read, even the typing heartbeat fired every few seconds
	// while someone composes - needs one stable value from it (the device id),
	// and used to reload the entire webapp page to get it, with its own retry
	// storm on top. A real client loads that page once per session.
	// fetchMu serialises page fetches; universalDataMu guards the stored value.
	// See getMessagesUniversalData for why they are separate.
	fetchMu         sync.Mutex
	universalDataMu sync.Mutex
	universalData   MessagesUniversalData
	universalDataAt time.Time
	// session holds the cookie header. It lives here rather than as a client
	// header because the header is applied per request, only for TikTok hosts
	// (see attachSessionCookie), it is updated from Set-Cookie as TikTok rotates
	// values, and several call sites need the raw value to build payloads.
	session *sessionStore

	// ttl is this client own cache refresh interval; see universalDataTTL.
	ttl time.Duration

	// gate is the account-wide throttle pause. See rateGate.
	gate rateGate

	// sendMu serialises outgoing messages so the pacing floor is per account
	// rather than per caller.
	sendMu   sync.Mutex
	lastSend time.Time
}

// sessionCookie returns the current cookie header, including any values TikTok
// has rotated since login.
func (c *Client) sessionCookie() string {
	if c.session == nil {
		return ""
	}
	return c.session.header()
}

// SessionCookie exposes the current cookie header so the bridge can persist it.
// Without persisting, every restart reverts to the values pasted at login and
// discards every rotation since.
func (c *Client) SessionCookie() string { return c.sessionCookie() }

// universalDataTTL is how long the cached /messages page is trusted before it is
// fetched again. The value callers actually read from it (wid) is a device
// identity, not a per-request token, so a long TTL is safe; refreshing every
// so often just keeps us honest if TikTok rotates it.
const universalDataTTL = 30 * time.Minute

// universalDataTTL returns this client's own refresh interval: the constant
// above, plus or minus up to 20%, fixed for the life of the client.
//
// One bridge process hosts several accounts. On a fixed interval, accounts
// started together — a restart, a deploy — refresh at the same instant and stay
// locked together indefinitely, so one IP emits N identical requests on a
// precise cycle. Spreading the interval per client breaks the lockstep.
func (c *Client) universalDataTTL() time.Duration {
	if c.ttl <= 0 {
		// A Client built as a zero value (tests, and any future construction
		// path that skips NewClient) must still get a usable cache rather than
		// one that counts as expired the instant it is written.
		return universalDataTTL
	}
	return c.ttl
}

func jitteredTTL() time.Duration {
	spread := (rand.Float64()*0.4 - 0.2) * float64(universalDataTTL)
	return universalDataTTL + time.Duration(spread)
}

// ThrottleBackoff returns how long to wait after a throttle: what the server
// asked for, plus a random margin.
//
// The margin is the point. A dozen operations paused by the same 429 that each
// wait exactly Retry-After all resume on the same tick, and the resulting burst
// is what produced the throttle in the first place.
func ThrottleBackoff(err error) time.Duration {
	base := defaultThrottleWait
	var rl *ErrRateLimited
	if errors.As(err, &rl) && rl.RetryAfter > 0 {
		base = rl.RetryAfter
	}
	if base > maxThrottleWait {
		base = maxThrottleWait
	}
	// Only ever added, never subtracted: resuming before the server said to
	// would defeat the purpose.
	return base + time.Duration(rand.Float64()*0.3*float64(base))
}

const (
	// defaultThrottleWait applies when a 429 arrives with no Retry-After.
	defaultThrottleWait = 30 * time.Second
	// maxThrottleWait bounds a hostile or mistaken Retry-After, so one bad
	// header cannot park the bridge for a day.
	maxThrottleWait = 15 * time.Minute
	// minSendInterval is the floor between two outgoing messages on one
	// account. Sending as fast as the API accepts is the pattern platforms ban
	// for; this is roughly the fastest a person sends separate messages.
	minSendInterval = 900 * time.Millisecond
)

// PaceOutgoing blocks until enough time has passed since the previous outgoing
// message on this account. It returns early if ctx is cancelled, so shutdown
// does not wait for a queue to drain.
//
// The lock is held across the wait deliberately: the limit belongs to the
// account, so concurrent senders have to queue behind each other rather than
// each observing their own gap.
func (c *Client) PaceOutgoing(ctx context.Context) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	wait := minSendInterval - time.Since(c.lastSend)
	if wait > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
	c.lastSend = time.Now()
}

type MessagesUniversalData map[string]any

func (m MessagesUniversalData) getAppContext() (map[string]any, error) {
	defaultScope, ok := m["__DEFAULT_SCOPE__"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("__DEFAULT_SCOPE__ not found or wrong type")
	}

	appContext, ok := defaultScope["webapp.app-context"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("webapp.app-context not found or wrong type")
	}

	return appContext, nil
}

// GetMessages fetches /messages, extracts the #__UNIVERSAL_DATA_FOR_REHYDRATION__
// script tag, and returns its contents as a parsed JSON map.
// It retries when TikTok serves a page with no hydration script. That happens
// often enough to matter — roughly one request in three — and this is the hot
// path for the whole bridge: both GetInbox and GetMessages start here, so a
// single miss meant an empty conversation list or a thread that failed to load,
// not merely a degraded one.
func (c *Client) getMessagesUniversalData() (MessagesUniversalData, error) {
	// Two locks, deliberately.
	//
	// fetchMu serialises the fetch, so a burst of callers that all miss the
	// cache produces one request rather than one each. universalDataMu only
	// guards the stored value and is held for the assignment, never across the
	// request — the error path invalidates the cache, and doing that while
	// holding the same lock the caller took deadlocks the client permanently
	// on any 401.
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	if data := c.cachedUniversalData(); data != nil {
		return data, nil
	}
	data, err := c.fetchMessagesUniversalData()
	if err != nil {
		return nil, err
	}
	c.universalDataMu.Lock()
	c.universalData = data
	c.universalDataAt = time.Now()
	c.universalDataMu.Unlock()
	return data, nil
}

// cachedUniversalData returns the cached page if it is still fresh.
func (c *Client) cachedUniversalData() MessagesUniversalData {
	c.universalDataMu.Lock()
	defer c.universalDataMu.Unlock()
	if c.universalData != nil && time.Since(c.universalDataAt) < c.universalDataTTL() {
		return c.universalData
	}
	return nil
}

// invalidateUniversalData drops the cached page so the next caller refetches.
// For when a request that depended on it comes back rejected.
func (c *Client) invalidateUniversalData() {
	c.universalDataMu.Lock()
	c.universalData = nil
	c.universalDataMu.Unlock()
}

// fetchMessagesUniversalData is the uncached load. See getMessagesUniversalData.
func (c *Client) fetchMessagesUniversalData() (MessagesUniversalData, error) {
	return c.fetchMessagesUniversalDataCtx(context.Background())
}

// fetchMessagesUniversalDataCtx loads and parses the messages page. It takes a
// context because the retry below can sleep for seconds: without one, shutdown
// waits for it and a reconnect can start while the previous attempt is still
// in flight.
func (c *Client) fetchMessagesUniversalDataCtx(ctx context.Context) (MessagesUniversalData, error) {
	var lastErr error
	for attempt := 1; attempt <= messagesPageAttempts; attempt++ {
		if attempt > 1 {
			// Backs off hard rather than linearly: the shell is a throttle, and
			// four rapid retries measurably provoked more of them than they
			// cleared. 0.6s, 1.8s, 5.4s gives TikTok time to let go.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(messagesPageRetryBase * time.Duration(pow3(attempt-2))):
			}
		}
		resp, err := c.r.R().
			SetContext(ctx).
			SetHeader("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8").
			Get("/messages")
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("get /messages: %w", err)
			continue
		}
		if err := c.checkHTTPResponse("get /messages", "", resp); err != nil {
			// Two answers are final. Retrying a throttle escalates it, and
			// re-presenting a credential the server just refused will be
			// refused again — three more times, in this loop. Everything else
			// here is a transient failure worth another attempt.
			if IsRateLimited(err) || IsAuthRejected(err) {
				return nil, err
			}
			lastErr = err
			continue
		}

		rawJSON, err := extractUniversalData(resp.String())
		if err != nil {
			lastErr = fmt.Errorf("extract universal data: %w", err)
			continue
		}

		var result map[string]any
		if err := json.Unmarshal([]byte(rawJSON), &result); err != nil {
			// Malformed JSON is not a throttle; retrying just repeats it.
			return nil, fmt.Errorf("parse universal data JSON: %w", err)
		}

		return result, nil
	}
	return nil, lastErr
}

// messagesPageAttempts bounds the retry above. Four tries turns a 1-in-3 miss
// rate into roughly 1 in 80.
const messagesPageAttempts = 4

// messagesPageRetryBase is the first backoff step; the schedule triples from
// here. A variable rather than a constant so tests can exercise the retry logic
// without sleeping the real several seconds per case.
var messagesPageRetryBase = 600 * time.Millisecond

// pow3 returns 3^n for small n, for the backoff schedule above.
func pow3(n int) int {
	result := 1
	for i := 0; i < n; i++ {
		result *= 3
	}
	return result
}

// extractUniversalData parses the HTML body and returns the raw JSON string
// contained in the <script id="__UNIVERSAL_DATA_FOR_REHYDRATION__"> tag.
func extractUniversalData(body string) (string, error) {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("parse HTML: %w", err)
	}

	var content string
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == "script" {
			for _, attr := range n.Attr {
				if attr.Key == "id" && attr.Val == "__UNIVERSAL_DATA_FOR_REHYDRATION__" {
					if n.FirstChild != nil {
						content = strings.TrimSpace(n.FirstChild.Data)
						return true
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if walk(child) {
				return true
			}
		}
		return false
	}
	walk(doc)

	if content == "" {
		return "", fmt.Errorf("script#__UNIVERSAL_DATA_FOR_REHYDRATION__ not found or empty")
	}
	return content, nil
}

// NewClient builds a TikTok client. proxyURL, when non-empty, routes every
// request through that proxy (http, https or socks5). Running many accounts
// through one bridge, they otherwise all egress from the bridge host's single
// IP; a per-login proxy lets each account leave from an address of its own.
func NewClient(cookieString, proxyURL string) *Client {
	r := resty.New()
	r.SetHeader("User-Agent", DefaultUserAgent)
	r.SetHeader("Accept-Language", "en-US,en;q=0.9")
	r.SetBaseURL("https://www.tiktok.com")

	// A browser announcing this User-Agent always sends the client hints and
	// fetch metadata below with it. Announcing Chrome without them is the exact
	// combination TikTok's WAF challenges — the documented reason the media
	// scraper further down uses a curl User-Agent instead. The client that
	// carries the session should not have the mismatch the scraper avoids.
	r.SetHeader("sec-ch-ua", secCHUA)
	r.SetHeader("sec-ch-ua-mobile", "?0")
	r.SetHeader("sec-ch-ua-platform", `"macOS"`)
	r.SetHeader("Sec-Fetch-Site", "none")
	r.SetHeader("Sec-Fetch-Mode", "navigate")
	r.SetHeader("Sec-Fetch-User", "?1")
	r.SetHeader("Sec-Fetch-Dest", "document")
	r.SetHeader("Upgrade-Insecure-Requests", "1")

	rIA := resty.New()
	rIA.SetHeader("User-Agent", DefaultUserAgent)
	rIA.SetHeader("Accept-Language", "en-US,en;q=0.9")
	rIA.SetHeader("Referer", "https://www.tiktok.com/")
	rIA.SetBaseURL("https://im-api-sg.tiktok.com")
	// The IM API is called by page script, not navigated to, so its fetch
	// metadata differs from the document request above.
	rIA.SetHeader("sec-ch-ua", secCHUA)
	rIA.SetHeader("sec-ch-ua-mobile", "?0")
	rIA.SetHeader("sec-ch-ua-platform", `"macOS"`)
	rIA.SetHeader("Sec-Fetch-Site", "same-site")
	rIA.SetHeader("Sec-Fetch-Mode", "cors")
	rIA.SetHeader("Sec-Fetch-Dest", "empty")

	// Public pages and media: deliberately a curl-style User-Agent and no
	// session, so TikTok's WAF does not issue a JS challenge (it reserves those
	// for browser-UA requests missing Sec-Fetch-* headers).
	scraper := resty.New()
	scraper.SetHeader("User-Agent", "curl/8.11.0")
	scraper.SetHeader("Accept", "*/*")

	for _, c := range []*resty.Client{r, rIA, scraper} {
		hardenClient(c)
		if proxyURL != "" {
			c.SetProxy(proxyURL)
		}
	}
	// One store shared by both authenticated clients, so a rotation seen on
	// either is used by both. The scraper never carries the session at all.
	session := newSessionStore(cookieString)
	attachSessionCookie(r, session)
	attachSessionCookie(rIA, session)

	client := &Client{
		r:       r,
		rIA:     rIA,
		scraper: scraper,
		ttl:     jitteredTTL(),
		// The same store the middleware above writes rotations into. Building a
		// second one here from the same string compiles, passes the tests, and
		// silently breaks the whole feature: the HTTP clients would follow
		// rotations while the WebSocket dial, the request payloads and the
		// persisted cookie all kept reading the original values.
		session: session,
	}

	// Every request waits out an account-wide throttle, rather than each call
	// site being trusted to remember. Attached to all three clients: a pause
	// earned by the API is not a licence for the media client to keep pulling.
	for _, rc := range []*resty.Client{r, rIA, scraper} {
		rc.OnBeforeRequest(func(_ *resty.Client, req *resty.Request) error {
			return client.gate.wait(req.Context())
		})
	}
	return client
}

// attachSessionCookie sends the session only to TikTok.
//
// Several download helpers take a URL that arrived inside a message — chosen by
// whoever is talking to the user — and fetch it with this client. Setting the
// cookie as a permanent client header put the live session on every one of
// those requests, so a crafted attachment URL pointing at any host handed that
// host the account. A middleware that looks at the resolved destination is the
// only place that can know where the bytes are actually going.
func attachSessionCookie(c *resty.Client, store *sessionStore) {
	if store == nil || store.header() == "" {
		return
	}
	c.OnBeforeRequest(func(_ *resty.Client, req *resty.Request) error {
		u, err := url.Parse(req.URL)
		if err != nil {
			// Unparsable target: withhold the credential rather than guess.
			return nil
		}
		if isTikTokHost(u.Host) {
			req.SetHeader("Cookie", store.header())
		}
		return nil
	})

	// Take back whatever TikTok rotates. A browser does this automatically;
	// pinning the values pasted at login meant sending an increasingly stale
	// msToken until the server stopped accepting it.
	c.OnAfterResponse(func(_ *resty.Client, resp *resty.Response) error {
		if resp == nil || resp.RawResponse == nil {
			return nil
		}
		if u, err := url.Parse(resp.Request.URL); err != nil || !isTikTokHost(u.Host) {
			return nil
		}
		store.apply(resp.Cookies())
		return nil
	})
}

// isTikTokHost reports whether host is TikTok or one of its subdomains. An
// empty host means a relative URL resolved against the client's base URL, which
// is always TikTok.
func isTikTokHost(host string) bool {
	if host == "" {
		return true
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, domain := range tiktokDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// tiktokDomains are the domains the session cookie may be sent to. Media and
// API traffic is spread across several of them.
var tiktokDomains = []string{
	"tiktok.com",
	"tiktokcdn.com",
	"tiktokcdn-us.com",
	"tiktokv.com",
	"byteoversea.com",
	"ibyteimg.com",
}

// Response and connection limits. resty configures none of these by default:
// no timeout at all, and Go's default of 2 idle connections per host but no cap
// on simultaneous ones.
const (
	// requestTimeout bounds a single request. Media downloads are the slowest
	// thing here and still finish well inside this.
	requestTimeout = 90 * time.Second
	// maxResponseBytes caps what one response can pull into memory. The bridge
	// is long-lived; an unbounded read is an availability bug.
	maxResponseBytes = 64 << 20 // 64 MiB
	// maxConnsPerHost keeps a burst of downloads from opening a connection
	// each. A browser opens roughly six.
	maxConnsPerHost = 8
)

func hardenClient(c *resty.Client) {
	c.SetTimeout(requestTimeout)

	// resty installs a cookie jar by default, which means every Set-Cookie is
	// absorbed twice: once by the session store above, once by the jar. Go then
	// appends the jar's copy to the explicit Cookie header, so requests went out
	// carrying the same name twice - and the two copies drift, because the store
	// only accepts a known set of rotating names and refuses deletions while the
	// jar accepts everything. One store, one copy, one header.
	c.SetCookieJar(nil)

	base, ok := c.GetClient().Transport.(*http.Transport)
	if !ok || base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	base.MaxConnsPerHost = maxConnsPerHost
	base.MaxIdleConnsPerHost = maxConnsPerHost
	base.ResponseHeaderTimeout = 30 * time.Second
	c.SetTransport(&limitedTransport{base: base, limit: maxResponseBytes})
}

// limitedTransport truncates response bodies at limit bytes. The cap has to sit
// here rather than in a response hook: by the time a hook runs the body is
// already fully in memory, which is the thing being prevented.
type limitedTransport struct {
	base  http.RoundTripper
	limit int64
}

func (t *limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &limitedBody{ReadCloser: resp.Body, remaining: t.limit}
	return resp, nil
}

type limitedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, fmt.Errorf("response body exceeds the %d byte limit", maxResponseBytes)
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func extractCookie(cookieStr, name string) string {
	for _, part := range strings.Split(cookieStr, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && strings.TrimSpace(kv[0]) == name {
			return strings.TrimSpace(kv[1])
		}
	}
	return ""
}
