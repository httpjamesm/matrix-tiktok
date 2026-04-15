package libtiktok

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-resty/resty/v2"
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
	// ── 1. Fetch the video page ──────────────────────────────────────────────
	// Use a minimal curl-like client with no session cookies and no browser UA.
	// A Chrome UA without the matching Sec-Fetch-* / Sec-CH-UA fingerprint
	// headers triggers TikTok's Slardar WAF JS challenge; a plain non-browser
	// UA bypasses it entirely since TikTok does not challenge curl clients.
	resp, err := newScraperClient().R().
		SetContext(ctx).
		Get(videoURL)
	if err != nil {
		return nil, "", fmt.Errorf("get video page %s: %w", videoURL, err)
	}
	if resp.IsError() {
		return nil, "", fmt.Errorf("video page %s returned HTTP %d", videoURL, resp.StatusCode())
	}

	// ── 2. Capture Set-Cookie headers from the response ──────────────────────
	cookieStr := responseCookiesToString(resp.Cookies())

	// ── 3. Pull the __UNIVERSAL_DATA_FOR_REHYDRATION__ JSON blob ────────────
	rawJSON, err := extractUniversalData(resp.String())
	if err != nil {
		return nil, "", fmt.Errorf("extract universal data from %s: %w", videoURL, err)
	}

	var scope map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &scope); err != nil {
		return nil, "", fmt.Errorf("parse universal data JSON: %w", err)
	}

	// ── 4. Navigate to video.playAddr ────────────────────────────────────────
	playURL, err := extractVideoPlayAddr(scope)
	if err != nil {
		return nil, "", fmt.Errorf("extract play URL from %s: %w", videoURL, err)
	}

	// ── 5. Download the media using the cookies we received ──────────────────
	mediaResp, err := newScraperClient().R().
		SetContext(ctx).
		SetHeader("Referer", videoURL).
		SetHeader("Cookie", cookieStr).
		Get(playURL)
	if err != nil {
		return nil, "", fmt.Errorf("download play URL %s: %w", playURL, err)
	}
	if mediaResp.IsError() {
		return nil, "", fmt.Errorf("play URL %s returned HTTP %d", playURL, mediaResp.StatusCode())
	}

	mime := normalizeContentType(mediaResp.Header().Get("Content-Type"), "video/mp4")

	return mediaResp.Body(), mime, nil
}

// DownloadSticker fetches a signed TikTok DM sticker asset directly from the
// CDN URL embedded in the message payload and returns the raw bytes plus MIME
// type. The URLs are already signed, so no TikTok session cookies are needed.
func (c *Client) DownloadSticker(ctx context.Context, stickerURL string) ([]byte, string, error) {
	resp, err := newScraperClient().R().
		SetContext(ctx).
		SetHeader("Referer", "https://www.tiktok.com/").
		Get(stickerURL)
	if err != nil {
		return nil, "", fmt.Errorf("download sticker %s: %w", stickerURL, err)
	}
	if resp.IsError() {
		return nil, "", fmt.Errorf("sticker URL %s returned HTTP %d", stickerURL, resp.StatusCode())
	}

	return resp.Body(), normalizeContentType(resp.Header().Get("Content-Type"), guessStickerMIMEFromURL(stickerURL)), nil
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
	resp, err := newScraperClient().R().
		SetContext(ctx).
		Get(videoURL)
	if err != nil {
		return nil, fmt.Errorf("get video page: %w", err)
	}
	fmt.Println(resp.String())
	if resp.IsError() {
		return nil, fmt.Errorf("video page returned HTTP %d", resp.StatusCode())
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

// newScraperClient returns a bare resty client suitable for fetching public
// TikTok pages. It deliberately uses a curl-style User-Agent and carries no
// session cookies so that TikTok's Slardar WAF does not issue a JS challenge
// (which it reserves for browser-UA requests that lack Sec-Fetch-* headers).
func newScraperClient() *resty.Client {
	c := resty.New()
	c.SetHeader("User-Agent", "curl/8.11.0")
	c.SetHeader("Accept", "*/*")
	return c
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
