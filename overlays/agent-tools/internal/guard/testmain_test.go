package guard

import (
	"os"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	// Whether trash is on PATH differs between hosts and the Nix build;
	// TestTrash sets it.
	trashOnPath = func() bool { return false }
	os.Exit(m.Run())
}
