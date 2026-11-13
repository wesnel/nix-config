package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/record"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir, cmd.Env = dir, append(gitenv.Without(os.Environ()), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@e")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// agent-plan visual, run in the worktree of a reviewed change, copies a
// diagram from the review record of a range it resolves there, and reads a
// tape from a file.
func TestVisualCommand(t *testing.T) {
	setup(t)
	plan := newPlan(t)
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "tip")
	base, head := gitIn(t, repo, "rev-parse", "HEAD~1"), gitIn(t, repo, "rev-parse", "HEAD")
	path, err := record.RangeFile("o", "r", base, head)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Update(path, func(r *record.Range) error {
		*r = record.Range{Repo: "o/r", Base: base, Head: head, Title: "Queue", Updated: time.Now(),
			Diagrams: []record.Diagram{{ID: "D1", Title: "Queue entry states", Verdict: "holds",
				JSON: `{"kind": "state", "title": "Queue entry states", "at": "` + head + `", "nodes": [` +
					`{"id": "N1", "label": "Queued", "cite": {"path": "q.go", "line": 1, "endLine": null, "symbol": "Queued"}, "planned": null}], "edges": []}`}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	code, out, errs := agentPlan(t, "", "visual", plan, "--branch", "loop", "--range", "HEAD~1..HEAD", "--diagram", "D1")
	if code != 0 || !strings.Contains(out, `"id": "V1"`) {
		t.Fatalf("diagram: %d %s %s", code, out, errs)
	}
	tape := filepath.Join(repo, "demo.tape")
	if err := os.WriteFile(tape, []byte("Type \"agent-plan status\"\nEnter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := agentPlan(t, "", "visual", plan, "--branch", "loop", "--tape", tape, "--caption", "Status"); code != 0 ||
		!strings.Contains(out, `"id": "V2"`) {
		t.Fatalf("tape: %d %s %s", code, out, errs)
	}
	if code, _, errs := agentPlan(t, "", "visual", plan, "V2", "--url", "http://github.com/user-attachments/assets/1a"); code != 2 ||
		!strings.Contains(errs, "refused") {
		t.Errorf("an http URL: %d %s", code, errs)
	}
	data, err := os.ReadFile(plan)
	if err != nil {
		t.Fatal(err)
	}
	want := "** Visuals\n*** Queue entry states\n:PROPERTIES:\n:CUSTOM_ID: V1\n:BRANCH: loop\n:DIAGRAM: D1\n:RANGE: " + base + ".." + head +
		"\n:VERDICT: holds\n:END:\n#+begin_src mermaid\nstateDiagram-v2\n    state \"Queued\" as N1\n#+end_src\n" +
		"*** Status\n:PROPERTIES:\n:CUSTOM_ID: V2\n:BRANCH: loop\n:END:\n#+begin_src tape\nType \"agent-plan status\"\nEnter\n#+end_src\n" +
		"** References\n"
	if !strings.Contains(string(data), want) {
		t.Errorf("plan:\n%s\nwant within it:\n%s", data, want)
	}
	for _, args := range [][]string{{"visual", plan, "--branch", "loop", "--range", "HEAD~1..HEAD"}, {"step", plan, "--caption", "x"}} {
		if code, _, _ := agentPlan(t, "", args...); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
}
