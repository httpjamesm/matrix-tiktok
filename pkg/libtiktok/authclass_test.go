package libtiktok

import (
	"context"
	"net/http"
	"testing"
)

// 401 means the session is dead. 403 does not, and must not tell a customer to
// reconnect an account that is still working.
func TestAuthClassification(t *testing.T) {
	cases := []struct {
		status     int
		rejected   bool
		restricted bool
	}{
		{http.StatusUnauthorized, true, false},
		{http.StatusForbidden, false, true},
	}
	for _, tc := range cases {
		h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
			w.WriteHeader(tc.status)
		})
		c := clientAgainst(h)
		_, _, err := c.DownloadCover(context.Background(), h.URL+"/x.jpg")
		if err == nil {
			t.Fatalf("%d produced no error", tc.status)
		}
		if got := IsAuthRejected(err); got != tc.rejected {
			t.Errorf("%d: IsAuthRejected = %v, want %v (%v)", tc.status, got, tc.rejected, err)
		}
		if got := IsAccessRestricted(err); got != tc.restricted {
			t.Errorf("%d: IsAccessRestricted = %v, want %v (%v)", tc.status, got, tc.restricted, err)
		}
	}
}

// A page that loads cleanly but carries no signed-in user is the real
// logged-out signal. It used to surface as an unclear parse failure, so the one
// condition that genuinely warrants "log in again" was the one not saying it.
func TestLoggedOutPageIsAuthRejection(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request, n int64) {
		_, _ = w.Write([]byte(`<html><script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">` +
			`{"__DEFAULT_SCOPE__":{"webapp.app-context":{"user":{"uid":"","uniqueId":""}}}}</script></html>`))
	})
	c := clientAgainst(h)
	_, err := c.GetSelf(context.Background())
	if err == nil {
		t.Fatal("a logged-out page produced no error")
	}
	if !IsAuthRejected(err) {
		t.Errorf("logged-out page surfaced as %v; the customer is never told to log in again", err)
	}
}
