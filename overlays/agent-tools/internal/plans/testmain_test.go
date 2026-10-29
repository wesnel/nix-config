package plans

import (
	"os"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	code := m.Run()
	if pushGuardDir != "" {
		os.RemoveAll(pushGuardDir)
	}
	os.Exit(code)
}
