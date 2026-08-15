package libtiktok

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	cases := map[string]time.Duration{
		"":        0,
		"garbage": 0,
		"-3":      0,
		"0":       0,
		"12":      12 * time.Second,
		" 4 ":     4 * time.Second,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
	future := time.Now().Add(60 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got < 55*time.Second || got > 61*time.Second {
		t.Errorf("HTTP-date gave %v, want ~60s", got)
	}
	past := time.Now().Add(-60 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Errorf("past HTTP-date gave %v, want 0", got)
	}
}

func TestUniversalDataCacheIsUsedAndInvalidated(t *testing.T) {
	c := &Client{}
	c.universalData = MessagesUniversalData{"marker": "cached"}
	c.universalDataAt = time.Now()

	// Within the TTL the cache answers without touching the network.
	got, err := c.getMessagesUniversalData()
	if err != nil || got["marker"] != "cached" {
		t.Fatalf("expected cache hit, got %v, %v", got, err)
	}

	// After invalidation the cache is gone.
	c.invalidateUniversalData()
	if c.universalData != nil {
		t.Fatal("expected cache to be cleared")
	}

	// A stale entry is not trusted.
	c.universalData = MessagesUniversalData{"marker": "stale"}
	c.universalDataAt = time.Now().Add(-universalDataTTL - time.Second)
	if c.universalData != nil && time.Since(c.universalDataAt) < universalDataTTL {
		t.Fatal("stale entry should be outside the TTL")
	}
}
