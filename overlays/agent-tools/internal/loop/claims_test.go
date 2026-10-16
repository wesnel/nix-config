package loop

import (
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/relay"
)

// open refuses a description whose verification claims no audit holds,
// before it asks GitHub anything; through the relay, a review loop's
// description with the section is refused whatever audits there are, for
// none of the loop's calls are in this host's evidence log.
func TestOpenClaims(t *testing.T) {
	f, _ := pushStack(t)
	loopPlan(t, nil, []string{"lower..upper"})
	body := "Adds c.\n\n## Verification beyond CI\n\n- go test passes\n"
	_, err := Open("upper", "lower", "Add c", body, true)
	if !refusedWell(err) || !strings.Contains(err.Error(), "git fresh-review audit") || len(f.calls) != 0 {
		t.Errorf("opened with unaudited claims: %v", err)
	}
	t.Setenv(relay.RelayEnv, "1")
	_, err = Open("upper", "lower", "Add c", body, true)
	if !refusedWell(err) || !strings.Contains(err.Error(), "operator") || strings.Contains(err.Error(), "git fresh-review audit") {
		t.Errorf("a loop opened with claims: %v", err)
	}
}
