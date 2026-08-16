package libtiktok

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"golang.org/x/net/html"
)

const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"

type Client struct {
	// r is the client for www.tiktok.com
	r *resty.Client
	// rIA is the client for the IM API specifically
	rIA *resty.Client

	// The parsed /messages page, kept for a while. Every action - sending,
	// reacting, marking read, even the typing heartbeat fired every few seconds
	// while someone composes - needs one stable value from it (the device id),
	// and used to reload the entire webapp page to get it, with its own retry
	// storm on top. A real client loads that page once per session.
	universalDataMu sync.Mutex
	universalData   MessagesUniversalData
	universalDataAt time.Time
}

// universalDataTTL is how long the cached /messages page is trusted before it is
// fetched again. The value callers actually read from it (wid) is a device
// identity, not a per-request token, so a long TTL is safe; refreshing every
// so often just keeps us honest if TikTok rotates it.
const universalDataTTL = 30 * time.Minute

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
	c.universalDataMu.Lock()
	defer c.universalDataMu.Unlock()
	if c.universalData != nil && time.Since(c.universalDataAt) < universalDataTTL {
		return c.universalData, nil
	}
	data, err := c.fetchMessagesUniversalData()
	if err != nil {
		return nil, err
	}
	c.universalData = data
	c.universalDataAt = time.Now()
	return data, nil
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
	var lastErr error
	for attempt := 1; attempt <= messagesPageAttempts; attempt++ {
		if attempt > 1 {
			// Backs off hard rather than linearly: the shell is a throttle, and
			// four rapid retries measurably provoked more of them than they
			// cleared. 0.6s, 1.8s, 5.4s gives TikTok time to let go.
			time.Sleep(600 * time.Millisecond * time.Duration(pow3(attempt-2)))
		}
		resp, err := c.r.R().
			SetHeader("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8").
			Get("/messages")
		if err != nil {
			lastErr = fmt.Errorf("get /messages: %w", err)
			continue
		}
		if resp.IsError() {
			lastErr = fmt.Errorf("get /messages: unexpected status %d", resp.StatusCode())
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
	r.SetHeader("Cookie", cookieString)
	r.SetHeader("User-Agent", DefaultUserAgent)
	r.SetHeader("Accept-Language", "en-US,en;q=0.9")
	r.SetBaseURL("https://www.tiktok.com")

	rIA := resty.New()
	rIA.SetHeader("Cookie", cookieString)
	rIA.SetHeader("User-Agent", DefaultUserAgent)
	rIA.SetHeader("Accept-Language", "en-US,en;q=0.9")
	rIA.SetHeader("Referer", "https://www.tiktok.com/")
	rIA.SetBaseURL("https://im-api-sg.tiktok.com")

	if proxyURL != "" {
		r.SetProxy(proxyURL)
		rIA.SetProxy(proxyURL)
	}
	return &Client{
		r:   r,
		rIA: rIA,
	}
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
