package claims

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/refusal"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = gitenv.Without(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

const gated = "Opens it.\n\n## Verification beyond CI\n\n- go test passes\n- it builds\n"

// The gate lets a description through where its section's claims, as they
// stand, have an audit of the change as it stands with every claim
// supported, in any of this host's plans; a description without the
// section passes, and a review loop's with it never does.
func TestGate(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "f"), []byte("a\n"), 0o644)
	gitIn(t, dir, "add", "f")
	gitIn(t, dir, "commit", "-qm", "Start")
	gitIn(t, dir, "switch", "-qc", "feature")
	os.WriteFile(filepath.Join(dir, "f"), []byte("a\nb\n"), 0o644)
	gitIn(t, dir, "commit", "-qam", "Grow f")
	tip := gitIn(t, dir, "rev-parse", "HEAD")

	c := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
	config := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(map[string]any{"plans": c})
	os.WriteFile(config, data, 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	made, err := c.New(plans.NewOptions{Title: "Grow f", Slug: "grow", Branches: []string{"main..feature"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	id, err := git.PatchID([]string{"-C", dir}, "main", tip)
	if err != nil {
		t.Fatal(err)
	}
	hash := Hash(Of(gated))
	receipt := func(t *testing.T, hash, patchID string, unsupported ...string) {
		t.Helper()
		if _, err := p.Receipt("", plans.Receipt{Range: "main..feature", Tip: tip, PatchID: patchID, Reviewer: "codex",
			Focus: plans.ClaimsFocus, Branch: "feature", FindingIDs: unsupported, Findings: len(unsupported), Claims: hash},
			time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	refusedWith := func(t *testing.T, err error, want string) {
		t.Helper()
		var r refusal.Refusal
		ok := err != nil && strings.Contains(err.Error(), want)
		if ok {
			ok = refusal.SaysInstead(err.Error()) && errors.As(err, &r)
		}
		if !ok {
			t.Errorf("got %v, want a refusal saying %q", err, want)
		}
	}

	if err := Gate(dir, tip, "No claims here.\n\n## Limits\n\n- none\n", false); err != nil {
		t.Errorf("a description without the section: %v", err)
	}
	// The audit to run, from wherever the session is.
	refusedWith(t, Gate(dir, tip, gated, false), "`cd WT && git fresh-review audit")
	refusedWith(t, Gate(dir, tip, gated, false), "--session")
	receipt(t, hash, id, "C2")
	refusedWith(t, Gate(dir, tip, gated, false), "C2")
	receipt(t, Hash(Of(gated+"- and more\n")), id)
	refusedWith(t, Gate(dir, tip, gated, false), "git fresh-review audit")
	receipt(t, hash, "0123456789abcdef0123456789abcdef01234567")
	refusedWith(t, Gate(dir, tip, gated, false), "git fresh-review audit")
	receipt(t, hash, id)
	if err := Gate(dir, tip, gated, false); err != nil {
		t.Errorf("an audited description: %v", err)
	}
	if err := Gate(dir, tip, strings.Replace(gated, "- it builds", "- it\n  builds", 1), false); err != nil {
		t.Errorf("rewrapped: %v", err)
	}
	refusedWith(t, Gate(dir, tip, gated, true), "operator")

	// A signing pass rewrites the commits and keeps the change.
	gitIn(t, dir, "commit", "-q", "--amend", "-m", "Grow f, reworded")
	if err := Gate(dir, gitIn(t, dir, "rev-parse", "HEAD"), gated, false); err != nil {
		t.Errorf("after a rewrite that kept the change: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "f"), []byte("a\nb\nc\n"), 0o644)
	gitIn(t, dir, "commit", "-qam", "Grow f more")
	refusedWith(t, Gate(dir, gitIn(t, dir, "rev-parse", "HEAD"), gated, false), "git fresh-review audit")
}
