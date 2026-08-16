package libtiktok

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// DownloadVideo fetches a TikTok video page, extracts the video play address
// from the embedded __DEFAULT_SCOPE__ JSON blob, downloads the video using the
// Set-Cookie headers received from the video page, and returns the raw bytes
// along with the MIME type reported by the media server.
//
// The path navigated is:
//
//	__DEFAULT_SCOPE__["webapp.video-detail"].itemInfo.itemStruct.video.playAddr
func (c *Client) DownloadVideo(ctx context.Context, videoURL string) ([]byte, string, error) {
	// ── 1-3. Fetch the page and pull the hydration blob, retrying ────────────
	scope, cookieStr, err := c.fetchVideoScopeWithRetry(ctx, videoURL)
	if err != nil {
		return nil, "", err
	}

	// ── 4. Navigate to video.playAddr ────────────────────────────────────────
	playURL, err := extractVideoPlayAddr(scope)
	if err != nil {
		return nil, "", fmt.Errorf("extract play URL from %s: %w", videoURL, err)
	}

	// ── 5. Download the media using the cookies we received ──────────────────
	mediaResp, err := c.scraper.R().
		SetContext(ctx).
		SetHeader("Referer", videoURL).
		SetHeader("Cookie", cookieStr).
		Get(playURL)
	if err != nil {
		return nil, "", fmt.Errorf("download play URL %s: %w", playURL, err)
	}
	if err := c.checkHTTPResponse("play", playURL, mediaResp); err != nil {
		return nil, "", err
	}

	mime := normalizeContentType(mediaResp.Header().Get("Content-Type"), "video/mp4")

	return mediaResp.Body(), mime, nil
}

// ErrVideoUnavailable reports a post TikTok will not serve to anyone — deleted,
// made private, or blocked in this region. Retrying cannot help, and callers
// should say so rather than reporting a generic download failure.
var ErrVideoUnavailable = errors.New("video is unavailable on TikTok")

// videoScopeAttempts is how many times a missing hydration blob is retried.
//
// TikTok serves the real page most of the time and a ~43KB shell with no
// hydration script the rest — measured at roughly one request in three, with no
// pattern by video, account or age. That shell was being treated as a permanent
// failure, so a third of every shared video silently degraded to a bare link.
// Three attempts takes a 1-in-3 miss rate to about 1 in 27.
const videoScopeAttempts = 4

// fetchVideoScopeWithRetry fetches the video page until the hydration blob is
// present, and returns the parsed scope plus the cookies the media host needs.
func (c *Client) fetchVideoScopeWithRetry(
	ctx context.Context,
	videoURL string,
) (map[string]any, string, error) {
	log := zerolog.Ctx(ctx)
	var lastErr error
	for attempt := 1; attempt <= videoScopeAttempts; attempt++ {
		if attempt > 1 {
			// Small linear backoff: the shell is a throttle, not a hard block,
			// and pausing briefly is what makes the retry worth anything.
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(time.Duration(attempt-1) * 400 * time.Millisecond):
			}
		}

		// Use a minimal curl-like client with no session cookies and no browser
		// UA. A Chrome UA without the matching Sec-Fetch-* / Sec-CH-UA
		// fingerprint headers triggers TikTok's Slardar WAF JS challenge; a
		// plain non-browser UA bypasses it entirely since TikTok does not
		// challenge curl clients. (Measured: a full browser UA gets a 1.4KB
		// block page every time, so this is still the right client.)
		resp, err := c.scraper.R().
			SetContext(ctx).
			Get(videoURL)
		if err != nil {
			lastErr = fmt.Errorf("get video page %s: %w", videoURL, err)
			continue
		}
		if err := c.checkHTTPResponse("video page", videoURL, resp); err != nil {
			// A throttle is not a transient failure: retrying is what turns it
			// into a block. Surface it and let the caller wait.
			if IsRateLimited(err) {
				return nil, "", err
			}
			lastErr = err
			continue
		}

		cookieStr := responseCookiesToString(resp.Cookies())

		rawJSON, err := extractUniversalData(resp.String())
		if err != nil {
			lastErr = fmt.Errorf("extract universal data from %s: %w", videoURL, err)
			log.Debug().Int("attempt", attempt).Int("body_bytes", len(resp.Body())).
				Msg("TikTok served a page with no hydration data; retrying")
			continue
		}

		var scope map[string]any
		if err := json.Unmarshal([]byte(rawJSON), &scope); err != nil {
			return nil, "", fmt.Errorf("parse universal data JSON: %w", err)
		}

		// A page can hydrate perfectly and still carry no post: TikTok reports
		// that as a status code on the detail block, with itemStruct absent.
		// Without this check the caller only sees "playAddr not found", which
		// reads like a parser bug rather than a deleted video.
		if code, ok := videoDetailStatus(scope); ok && code != 0 {
			return nil, "", fmt.Errorf("%w (status %d): %s", ErrVideoUnavailable, code, videoURL)
		}

		return scope, cookieStr, nil
	}
	return nil, "", lastErr
}

// videoDetailStatus reads __DEFAULT_SCOPE__["webapp.video-detail"].statusCode.
// Observed: 0 for a healthy post, 10204 for one that is gone or restricted.
func videoDetailStatus(scope map[string]any) (int, bool) {
	defaultScope, ok := scope["__DEFAULT_SCOPE__"].(map[string]any)
	if !ok {
		return 0, false
	}
	detail, ok := defaultScope["webapp.video-detail"].(map[string]any)
	if !ok {
		return 0, false
	}
	code, ok := detail["statusCode"].(float64)
	if !ok {
		return 0, false
	}
	return int(code), true
}

// DownloadCover fetches a signed cover/thumbnail URL straight from the CDN. The
// URLs TikTok puts in a DM payload are pre-signed, so no session is needed —
// same arrangement as stickers.
func (c *Client) DownloadCover(ctx context.Context, coverURL string) ([]byte, string, error) {
	resp, err := c.scraper.R().
		SetContext(ctx).
		SetHeader("Referer", "https://www.tiktok.com/").
		Get(coverURL)
	if err != nil {
		return nil, "", fmt.Errorf("download cover %s: %w", coverURL, err)
	}
	if err := c.checkHTTPResponse("cover", coverURL, resp); err != nil {
		return nil, "", err
	}
	return resp.Body(), normalizeContentType(resp.Header().Get("Content-Type"), "image/jpeg"), nil
}

// DownloadSticker fetches a signed TikTok DM sticker asset directly from the
// CDN URL embedded in the message payload and returns the raw bytes plus MIME
// type. The URLs are already signed, so no TikTok session cookies are needed.
func (c *Client) DownloadSticker(ctx context.Context, stickerURL string) ([]byte, string, error) {
	resp, err := c.scraper.R().
		SetContext(ctx).
		SetHeader("Referer", "https://www.tiktok.com/").
		Get(stickerURL)
	if err != nil {
		return nil, "", fmt.Errorf("download sticker %s: %w", stickerURL, err)
	}
	if err := c.checkHTTPResponse("sticker", stickerURL, resp); err != nil {
		return nil, "", err
	}

	return resp.Body(), normalizeContentType(resp.Header().Get("Content-Type"), guessStickerMIMEFromURL(stickerURL)), nil
}

// DownloadPrivateImage fetches an authenticated private-image CDN URL using the
// logged-in TikTok browser session, decrypts the returned blob with AES-256-GCM,
// and returns the plaintext image bytes plus a detected image MIME type.
func (c *Client) DownloadPrivateImage(ctx context.Context, imageURL, decryptKey string) ([]byte, string, error) {
	resp, err := c.r.R().
		SetContext(ctx).
		SetHeader("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8").
		SetHeader("Origin", "https://www.tiktok.com").
		SetHeader("Referer", "https://www.tiktok.com/messages?lang=en").
		Get(imageURL)
	if err != nil {
		return nil, "", fmt.Errorf("download private image %s: %w", imageURL, err)
	}
	if err := c.checkHTTPResponse("private image", imageURL, resp); err != nil {
		return nil, "", err
	}

	plaintext, err := decryptTikTokPrivateImageBlob(decryptKey, resp.Body())
	if err != nil {
		return nil, "", fmt.Errorf("decrypt private image %s: %w", imageURL, err)
	}

	return plaintext, detectImageMIME(plaintext), nil
}

// DownloadPrivateVideo fetches an authenticated private-video CDN URL using the
// logged-in TikTok browser session and returns the raw bytes plus MIME type.
func (c *Client) DownloadPrivateVideo(ctx context.Context, videoURL string) ([]byte, string, error) {
	resp, err := c.r.R().
		SetContext(ctx).
		SetHeader("Accept", "video/*,*/*;q=0.8").
		SetHeader("Origin", "https://www.tiktok.com").
		SetHeader("Referer", "https://www.tiktok.com/messages?lang=en").
		Get(videoURL)
	if err != nil {
		return nil, "", fmt.Errorf("download private video %s: %w", videoURL, err)
	}
	if err := c.checkHTTPResponse("private video", videoURL, resp); err != nil {
		return nil, "", err
	}

	return resp.Body(), normalizeContentType(resp.Header().Get("Content-Type"), guessPrivateVideoMIMEFromURL(videoURL)), nil
}

// extractVideoPlayAddr navigates the parsed __DEFAULT_SCOPE__ map returned by
// the TikTok video detail page to pull out the nested video play address.
//
//	__DEFAULT_SCOPE__
//	  └─ webapp.video-detail
//	       └─ itemInfo
//	            └─ itemStruct
//	                 └─ video
//	                      └─ playAddr  ← this
func extractVideoPlayAddr(data map[string]any) (string, error) {
	// obj is a small helper that type-asserts a nested key as map[string]any.
	obj := func(m map[string]any, key string) (map[string]any, error) {
		v, ok := m[key]
		if !ok {
			return nil, fmt.Errorf("%q key not found", key)
		}
		sub, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%q is not an object (got %T)", key, v)
		}
		return sub, nil
	}

	defaultScope, err := obj(data, "__DEFAULT_SCOPE__")
	if err != nil {
		return "", err
	}
	videoDetail, err := obj(defaultScope, "webapp.video-detail")
	if err != nil {
		return "", err
	}
	itemInfo, err := obj(videoDetail, "itemInfo")
	if err != nil {
		return "", err
	}
	itemStruct, err := obj(itemInfo, "itemStruct")
	if err != nil {
		return "", err
	}
	video, err := obj(itemStruct, "video")
	if err != nil {
		return "", err
	}

	playURL, ok := video["playAddr"].(string)
	if !ok || playURL == "" {
		return "", fmt.Errorf("video.playAddr not found or empty")
	}
	return playURL, nil
}

// FetchVideoScope fetches a TikTok video page and returns the fully-parsed
// __UNIVERSAL_DATA_FOR_REHYDRATION__ JSON blob as a plain map. This is the
// same data DownloadVideo navigates internally; exposing it here lets callers
// (e.g. the libtiktok-test CLI) inspect exactly what is — and is not — present
// without having to reproduce the fetch logic themselves.
func (c *Client) FetchVideoScope(ctx context.Context, videoURL string) (map[string]any, error) {
	log := zerolog.Ctx(ctx).With().Str("component", "libtiktok-video-scope").Logger()
	resp, err := c.scraper.R().
		SetContext(ctx).
		Get(videoURL)
	if err != nil {
		return nil, fmt.Errorf("get video page: %w", err)
	}
	if parsed, parseErr := url.Parse(videoURL); parseErr == nil {
		log.Debug().
			Str("video_host", parsed.Host).
			Str("video_path", parsed.Path).
			Int("status_code", resp.StatusCode()).
			Int("body_bytes", len(resp.Body())).
			Msg("Fetched TikTok video page for scope inspection")
	} else {
		log.Debug().
			Int("status_code", resp.StatusCode()).
			Int("body_bytes", len(resp.Body())).
			Msg("Fetched TikTok video page for scope inspection")
	}
	if err := c.checkHTTPResponse("video page", videoURL, resp); err != nil {
		return nil, err
	}

	rawJSON, err := extractUniversalData(resp.String())
	if err != nil {
		return nil, fmt.Errorf("extract universal data: %w", err)
	}

	var scope map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &scope); err != nil {
		return nil, fmt.Errorf("parse universal data JSON: %w", err)
	}
	return scope, nil
}

// responseCookiesToString converts a slice of *http.Cookie (as returned by a
// resty response's Cookies() method) into a semicolon-separated "name=value"
// string ready to be used as a Cookie request header.
func responseCookiesToString(cookies []*http.Cookie) string {
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

func normalizeContentType(contentType, fallback string) string {
	if contentType == "" {
		return fallback
	}
	if idx := strings.Index(contentType, ";"); idx != -1 {
		contentType = strings.TrimSpace(contentType[:idx])
	}
	if contentType == "" {
		return fallback
	}
	return contentType
}

func decryptTikTokPrivateImageBlob(hexKey string, blob []byte) ([]byte, error) {
	if len(blob) < 12+16 {
		return nil, fmt.Errorf("blob too short for AES-GCM: got %d bytes", len(blob))
	}

	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("decode decrypt_key hex: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("decrypt_key must decode to 32 bytes, got %d", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM: %w", err)
	}

	nonce := blob[:12]
	ctTag := blob[12:]
	plaintext, err := aead.Open(nil, nonce, ctTag, nil)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM authentication failed: %w", err)
	}
	return plaintext, nil
}

func detectImageMIME(data []byte) string {
	if len(data) == 0 {
		return "image/jpeg"
	}

	mime := http.DetectContentType(data)
	if mime == "application/octet-stream" {
		return "image/jpeg"
	}
	return mime
}

func guessPrivateVideoMIMEFromURL(u string) string {
	lower := strings.ToLower(u)
	switch {
	case strings.Contains(lower, "mime_type=video_webm"):
		return "video/webm"
	case strings.Contains(lower, "mime_type=video_quicktime"):
		return "video/quicktime"
	default:
		return "video/mp4"
	}
}
