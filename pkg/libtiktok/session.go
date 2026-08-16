package libtiktok

import (
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Session cookie lifecycle.
//
// The login flow captures one Cookie header pasted out of a browser and the
// bridge used it unchanged for the life of the login. TikTok does not treat
// those values as constants: it rotates msToken on essentially every response,
// and refreshes ttwid and the csrf token periodically, via Set-Cookie. A real
// client sends back whatever it was last given.
//
// Pinning the original had three costs. The msToken sent with every IM request
// drifted further out of date until the server stopped accepting it, at which
// point the only recovery was the user pasting a fresh cookie by hand. A value
// that never changes across weeks of traffic is also, on its own, a way to tell
// a client apart from a browser. And any server-side rotation intended as a
// security measure never took effect.

// cookieNames that TikTok rotates and that must be sent back updated.
// Everything else in the header is left exactly as pasted.
var rotatingCookies = map[string]bool{
	"msToken":        true,
	"ttwid":          true,
	"tt_csrf_token":  true,
	"tt_chain_token": true,
	"odin_tt":        true,
	"sid_guard":      true,
	"sessionid":      true,
	"sessionid_ss":   true,
	"sid_tt":         true,
}

// sessionStore holds the current cookie header and applies rotations.
type sessionStore struct {
	mu     sync.RWMutex
	values map[string]string
	// order preserves the sequence the values were first seen in, so the header
	// this produces stays stable rather than reshuffling every request.
	order []string
}

func newSessionStore(cookieHeader string) *sessionStore {
	s := &sessionStore{values: map[string]string{}}
	for _, part := range strings.Split(cookieHeader, ";") {
		name, value, found := strings.Cut(strings.TrimSpace(part), "=")
		name = strings.TrimSpace(name)
		if !found || name == "" {
			continue
		}
		if _, seen := s.values[name]; !seen {
			s.order = append(s.order, name)
		}
		s.values[name] = strings.TrimSpace(value)
	}
	return s
}

// header renders the current cookie header.
func (s *sessionStore) header() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	parts := make([]string, 0, len(s.order))
	for _, name := range s.order {
		parts = append(parts, name+"="+s.values[name])
	}
	return strings.Join(parts, "; ")
}

// get returns one cookie value.
func (s *sessionStore) get(name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.values[name]
}

// apply takes the Set-Cookie values from a response and updates the ones TikTok
// is allowed to rotate.
//
// Only known-rotating names are accepted. A response cannot introduce arbitrary
// new cookies into a session that is used to authenticate, and cannot delete
// one either — an expired-cookie instruction would otherwise let a single odd
// response log the bridge out.
func (s *sessionStore) apply(cookies []*http.Cookie) (changed []string) {
	if len(cookies) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range cookies {
		if c == nil || c.Value == "" || !rotatingCookies[c.Name] {
			continue
		}
		if s.values[c.Name] == c.Value {
			continue
		}
		if _, seen := s.values[c.Name]; !seen {
			s.order = append(s.order, c.Name)
		}
		s.values[c.Name] = c.Value
		changed = append(changed, c.Name)
	}
	sort.Strings(changed)
	return changed
}

// RequiredLoginCookies are the values a pasted Cookie header must contain for
// the session to be usable.
var RequiredLoginCookies = []string{"sessionid", "tt_csrf_token"}

// ValidateLoginCookies reports what is wrong with a pasted cookie header, in
// terms the person pasting it can act on.
//
// Without this the first sign of a bad paste is an opaque HTTP failure from a
// later API call, which reads as "the bridge is broken" rather than "that was
// the wrong header".
func ValidateLoginCookies(cookieHeader string) error {
	trimmed := strings.TrimSpace(cookieHeader)
	if trimmed == "" {
		return &LoginCookieError{Reason: "no cookies were provided"}
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "cookie:") {
		return &LoginCookieError{Reason: `the value still starts with "Cookie:" — paste only what comes after it`}
	}
	if !strings.Contains(trimmed, "=") {
		return &LoginCookieError{Reason: "that does not look like a cookie header (no name=value pairs found)"}
	}

	store := newSessionStore(trimmed)
	var missing []string
	for _, name := range RequiredLoginCookies {
		if store.get(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return &LoginCookieError{
			Reason: "the cookie header is missing " + strings.Join(missing, " and ") +
				" — copy it from a tab where you are logged in to tiktok.com",
			Missing: missing,
		}
	}
	return nil
}

// LoginCookieError describes an unusable cookie header.
type LoginCookieError struct {
	Reason  string
	Missing []string
}

func (e *LoginCookieError) Error() string { return e.Reason }
