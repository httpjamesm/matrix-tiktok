package libtiktok

import (
	"net/http"
	"strings"
	"testing"
)

// The store the request middleware writes rotations into must be the same one
// every reader uses. Two stores built from one string compile, pass the older
// tests, and silently disable the whole rotation feature.
func TestRotationReachesReaders(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		http.SetCookie(w, &http.Cookie{Name: "msToken", Value: "ROTATED"})
		_, _ = w.Write([]byte(`<html><script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">{"ok":1}</script></html>`))
	})
	c := NewClient("sessionid=abc; msToken=ORIGINAL", "")
	// Point at the test server but keep the middleware's host check satisfied.
	c.r.SetBaseURL(h.URL)
	c.session.apply([]*http.Cookie{{Name: "msToken", Value: "ROTATED"}})

	got := c.SessionCookie()
	if !strings.Contains(got, "msToken=ROTATED") {
		t.Errorf("readers still see the pre-rotation session: %q", got)
	}
}

// Exactly one Cookie header, with each name once. resty installs a cookie jar
// by default, so responses were absorbed twice — once by the session store and
// once by the jar — and Go appends jar cookies to the explicit header.
func TestNoDuplicateCookies(t *testing.T) {
	var seen []string
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		seen = append(seen, r.Header.Get("Cookie"))
		http.SetCookie(w, &http.Cookie{Name: "ttwid", Value: "T1"})
		http.SetCookie(w, &http.Cookie{Name: "msToken", Value: "M1"})
		_, _ = w.Write([]byte("ok"))
	})
	c := clientAgainst(h)
	for i := 0; i < 3; i++ {
		_, _, _ = c.DownloadCover(t.Context(), h.URL+"/x.jpg")
	}
	for i, hdr := range seen {
		for _, name := range []string{"ttwid", "msToken", "sessionid"} {
			if strings.Count(hdr, name+"=") > 1 {
				t.Errorf("request %d repeated %s in one Cookie header: %q", i+1, name, hdr)
			}
		}
	}
}
