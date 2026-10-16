package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/claims"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
)

// gh pr create and gh pr edit go ahead with a description whose
// verification claims an audit holds for the change checked out, read from
// the file --body-file names; a body the guard cannot read, or one given
// inline with the section, is refused.
func TestClaimsBody(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	if err := fixtureGits([]string{dir, "init", "-q", "-b", "main"}, []string{dir, "commit", "-q", "--allow-empty", "-m", "Start"},
		[]string{dir, "switch", "-qc", "feature"}, []string{dir, "commit", "-q", "--allow-empty", "-m", "Do it"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "f"), []byte("x\n"), 0o644)
	if err := fixtureGits([]string{dir, "add", "f"}, []string{dir, "commit", "-qm", "Add f"}); err != nil {
		t.Fatal(err)
	}
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	other := filepath.Join(parent, "other")
	if err := fixtureGits([]string{dir, "remote", "add", "origin", "https://github.com/o/r.git"},
		[]string{dir, "worktree", "add", "-q", "-b", "other", other, "main"}); err != nil {
		t.Fatal(err)
	}
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "gh"), []byte(fakePRView), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	body := "Does it.\n\n## Verification beyond CI\n\n- go test passes\n"
	os.WriteFile(filepath.Join(dir, "body.md"), []byte(body), 0o644)
	os.WriteFile(filepath.Join(dir, "plain.md"), []byte("Does it.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "plain2.md"), []byte("Does it.\n"), 0o644)
	elsewhere := filepath.Join(t.TempDir(), "plain.md")
	os.WriteFile(elsewhere, []byte("Does it.\n"), 0o644)
	abs := filepath.Join(dir, "body.md")
	rel, _ := filepath.Rel(other, dir)
	c := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
	config := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(map[string]any{"plans": c})
	os.WriteFile(config, data, 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	made, err := c.New(plans.NewOptions{Title: "Do it", Slug: "do", Branches: []string{"main..feature"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	check := func(t *testing.T, cwd, command, want string) {
		t.Helper()
		t.Chdir(cwd)
		input, _ := json.Marshal(map[string]string{"command": command})
		reason := CheckBaseline("Bash", "", input)
		switch {
		case want == "" && reason != "":
			t.Errorf("in %s, %q denied: %s", cwd, command, reason)
		case want != "" && !strings.Contains(reason, want):
			t.Errorf("in %s, %q: reason %q, want one saying %q", cwd, command, reason, want)
		case reason != "" && !saysInstead(reason):
			t.Errorf("%q: reason %q does not say what to do instead", command, reason)
		}
	}
	// Each is judged against the change of pull request 1, branch feature:
	// in its worktree, through a leading cd, or, by the pull request or
	// branch it names, from another worktree or no repository.
	type at struct{ cwd, command string }
	unaudited := []at{
		{dir, "gh pr create --draft --title t --body-file body.md"},
		{dir, "gh pr create --draft --title t -F body.md"},
		{dir, "gh pr create --draft --title t -Fbody.md"},
		{dir, "gh pr create --draft --title t --body-file=" + abs},
		{dir, "gh pr edit 1 --body-file body.md"},
		{other, "cd " + dir + " && gh pr edit --body-file body.md"},
		{other, "cd " + rel + " && gh pr edit 1 --body-file body.md"},
		{other, "gh pr edit 1 --body-file " + abs},
		{other, "gh pr edit feature --body-file " + abs},
		{other, "gh pr create --draft --title t --head feature --body-file " + abs},
		{outside, "cd " + dir + " && gh pr edit 1 --body-file body.md"},
		{outside, "gh pr edit 1 -R o/r --body-file " + abs},
		{outside, "gh pr edit https://github.com/o/r/pull/1 --body-file " + abs},
	}
	for _, c := range unaudited {
		check(t, c.cwd, c.command, "git fresh-review audit")
	}
	// A description without claims goes out whatever the form.
	for _, c := range []at{
		{dir, "gh pr create --draft --title t --body-file plain.md"},
		{dir, "gh pr create --draft --title t --body-file " + elsewhere},
		{dir, "gh pr create --draft --title t --body b"},
		{dir, "gh pr create --draft --title t --fill"},
		{dir, "gh pr edit 1 --title t"},
		{dir, "cp body.md b2.md && gh pr create --draft --title t --body-file plain.md"},
		{other, "cd " + dir + " && gh pr edit 1 --body-file plain.md"},
		{outside, "gh pr edit 1 --body-file " + elsewhere},
		{outside, "gh pr edit 9 --body-file " + elsewhere},
	} {
		check(t, c.cwd, c.command, "")
	}
	refused := []at{
		{dir, "gh pr create --draft --title t --body $'x\\n\\n## Verification beyond CI\\n\\n- y'"},
		{dir, "gh pr create --draft --title t --body 'x\n\n## Verification beyond CI\n\n- y'"},
		{dir, `gh pr create --draft --title t --body "$(cat body.md)"`},
		{dir, "gh pr create --draft --title t --body-file -"},
		{dir, "gh pr create --draft --title t --body-file missing.md"},
		{dir, "gh pr create --draft --title t --template body.md"},
		{dir, "gh pr create --draft --title t --editor"},
		{dir, "gh pr create --draft --title t --web"},
		{dir, "cp body.md plain2.md && gh pr create --draft --title t --body-file plain2.md"},
		{dir, "cp plain.md b3.md && gh pr create --draft --title t --body-file body.md"},
		{other, "gh pr edit 9 --body-file " + abs},
		{other, "gh pr edit gone --body-file " + abs},
		{outside, "gh pr edit --body-file " + elsewhere + " 1 && gh pr edit 1 --body-file " + abs},
	}
	for _, c := range refused {
		// A command that works from anywhere: the call after a cd to the
		// pull request's worktree.
		check(t, c.cwd, c.command, "`cd ")
	}

	p, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	id, err := git.PatchID([]string{"-C", dir}, "main", "feature")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Receipt("", plans.Receipt{Range: "main..feature", Tip: "feature", PatchID: id, Reviewer: "codex",
		Focus: plans.ClaimsFocus, Branch: "feature", Claims: claims.Hash(claims.Of(body))}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, c := range unaudited {
		check(t, c.cwd, c.command, "")
	}
	for _, c := range refused {
		check(t, c.cwd, c.command, "`cd ")
	}
}

// fakePRView stands in for `gh pr view`: pull request 1 is from branch
// feature, and no other is found.
const fakePRView = `#!/bin/sh
if [ "$1 $2 $3" = "pr view 1" ] || [ "$1 $2 $3" = "pr view https://github.com/o/r/pull/1" ]; then
  echo '{"headRefName": "feature"}'; exit 0
fi
echo "fake gh: no pull request for $*" >&2; exit 1
`
