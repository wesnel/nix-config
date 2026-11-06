package signpass

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	cleanup := gitenvtest.IsolateUser()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// aloneVar names, in the environment of a process alone starts, the one
// test that process runs.
const aloneVar = "SIGNPASS_TEST_ALONE"

// alone runs the calling test by itself in a new process of this test
// binary, for a test that sends this process a signal or changes how it
// handles one: either reaches every pass running beside it, as the pass
// listens for signals process-wide. In that process alone returns true and
// the test goes on. Here it runs in parallel, waits for that process, fails
// the test with its output unless it passed, and returns false.
func alone(t *testing.T) bool {
	t.Helper()
	if os.Getenv(aloneVar) == t.Name() {
		return true
	}
	t.Parallel()
	var pattern []string
	for _, part := range strings.Split(t.Name(), "/") {
		pattern = append(pattern, "^"+regexp.QuoteMeta(part)+"$")
	}
	cmd := exec.CommandContext(t.Context(), testBinary(t), "-test.run", strings.Join(pattern, "/"), "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), aloneVar+"="+t.Name())
	out, err := cmd.CombinedOutput()
	switch {
	case err == nil && strings.Contains(string(out), "--- SKIP: "+t.Name()+" ("):
		t.Skipf("skipped alone:\n%s", out)
	case err != nil || !strings.Contains(string(out), "--- PASS: "+t.Name()+" ("):
		t.Fatalf("alone: %v\n%s", err, out)
	}
	return false
}
