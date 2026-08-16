package libtiktok

import "testing"

// NewClient must accept a proxy and still build both HTTP clients, and an empty
// proxy must leave the client usable. resty keeps the proxy on an unexported
// field, so this checks construction rather than the stored value.
func TestNewClientProxy(t *testing.T) {
	if c := NewClient("cookie=1", "http://127.0.0.1:8080"); c == nil || c.r == nil || c.rIA == nil {
		t.Fatal("proxied client not fully constructed")
	}
	if c := NewClient("cookie=1", ""); c == nil || c.r == nil || c.rIA == nil {
		t.Fatal("unproxied client not fully constructed")
	}
}
