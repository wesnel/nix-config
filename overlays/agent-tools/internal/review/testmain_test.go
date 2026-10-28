package review

import (
	"os"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	if os.Getenv("REVIEW_ALLOWANCE_HELPER") == "1" {
		os.Exit(allowanceTestServer())
	}
	gitenv.Isolate()
	os.Exit(m.Run())
}
