package reviewer

import (
	"os"
	"testing"
	"time"
)

// TestMain runs before all tests in the reviewer package. It sets a very short
// retry delay so tests that exercise the retry path (e.g. NoCandidates) finish
// in milliseconds rather than seconds.
func TestMain(m *testing.M) {
	retryBaseDelay = time.Millisecond
	os.Exit(m.Run())
}
