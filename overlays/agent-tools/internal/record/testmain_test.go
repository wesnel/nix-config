package record

import (
	"os"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// Timestamps render in the host's zone, so the golden files hold UTC's.
func TestMain(m *testing.M) {
	gitenv.Isolate()
	time.Local = time.UTC
	os.Exit(m.Run())
}
