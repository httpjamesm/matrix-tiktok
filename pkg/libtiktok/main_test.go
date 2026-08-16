package libtiktok

import (
	"os"
	"testing"
	"time"
)

// The retry schedule is several seconds per attempt in production, which is
// correct there and pointless here: the tests assert on how many requests go
// out and in what order, not on the wall-clock gap. Shrinking it keeps the
// suite fast enough to run on every change.
func TestMain(m *testing.M) {
	messagesPageRetryBase = time.Millisecond
	os.Exit(m.Run())
}
