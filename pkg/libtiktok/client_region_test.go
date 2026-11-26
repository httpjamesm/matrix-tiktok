package libtiktok

import "testing"

func udWithDomains(api, ws string) MessagesUniversalData {
	return MessagesUniversalData{
		"__DEFAULT_SCOPE__": map[string]any{
			"webapp.biz-context": map[string]any{
				"domains": map[string]any{"imApi": api, "imFrontier": ws},
			},
		},
	}
}

func TestIMHostsFollowPage(t *testing.T) {
	ud := udWithDomains("https://im-api.tiktok.com", "wss://im-ws.tiktok.com/ws/v2")
	if got := ud.imAPIBase(); got != "https://im-api.tiktok.com" {
		t.Errorf("imAPIBase = %q", got)
	}
	if got := ud.imWSURL(); got != "wss://im-ws.tiktok.com/ws/v2" {
		t.Errorf("imWSURL = %q", got)
	}
}

func TestIMHostsFallBack(t *testing.T) {
	cases := []MessagesUniversalData{
		{},
		udWithDomains("", ""),
		udWithDomains("https://im-api.evil.example", "wss://evil.example/ws/v2"),
		udWithDomains("http://im-api.tiktok.com", "ws://im-ws.tiktok.com/ws/v2"),
		udWithDomains("https://im-api.tiktok.com.evil.example", "wss://x@im-ws.tiktok.com/ws/v2"),
	}
	for i, ud := range cases {
		if got := ud.imAPIBase(); got != defaultIMAPIBase {
			t.Errorf("case %d: imAPIBase = %q, want fallback", i, got)
		}
		if got := ud.imWSURL(); got != defaultIMWSURL {
			t.Errorf("case %d: imWSURL = %q, want fallback", i, got)
		}
	}
}
