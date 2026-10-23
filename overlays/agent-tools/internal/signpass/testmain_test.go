package signpass

import (
	"os"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	// Tests that stop at a missed touch with no terminal do not wait for a
	// retry; those of retrying set their own (withRetries).
	Retries = 0
	os.Exit(m.Run())
}
