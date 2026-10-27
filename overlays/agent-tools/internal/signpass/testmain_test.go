package signpass

import (
	"os"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	cleanup := gitenvtest.IsolateUser()
	// Tests that stop at a missed touch with no terminal do not wait for a
	// retry; those of retrying set their own (withRetries).
	Retries = 0
	code := m.Run()
	cleanup()
	os.Exit(code)
}
