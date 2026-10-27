package signpass

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/plans"
)

func TestPlanEnvironment(t *testing.T) {
	home := os.Getenv("HOME")
	temp, _ := filepath.EvalSymlinks(os.TempDir())
	realHome, _ := filepath.EvalSymlinks(home)
	underTemp, err := filepath.Rel(temp, realHome)
	if err != nil || underTemp == ".." || strings.HasPrefix(underTemp, ".."+string(filepath.Separator)) {
		t.Fatal("test home is not temporary")
	}
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "AGENT_TOOLS_CONFIG"} {
		value := os.Getenv(name)
		rel, err := filepath.Rel(home, value)
		if !filepath.IsAbs(value) || err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("%s is not isolated under the test home", name)
		}
	}
	c, err := plans.Load()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(home, c.Dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatal("plan directory escapes test home")
	}
}
