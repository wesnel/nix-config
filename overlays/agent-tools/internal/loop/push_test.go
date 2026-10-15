package loop

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/relay"
)

// fakeRefs answers what Push asks GitHub, for the viewer "me" in o/r, from a
// bare repository standing in for GitHub's copy: the open PRs, with their
// heads read from it; Git objects, created in it and named by the SHA they
// hash to, as GitHub does; and updateRefs, which like GitHub moves each ref
// only from its beforeOid. It is a fake because a test must not reach
// GitHub.
type fakeRefs struct {
	bare  string
	prs   map[string]int // head branch to PR number
	bases map[string]string
	// calls is each updateRefs call's refs, in the order given.
	calls [][]string
	// failCall, when not 0, is the updateRefs call that fails.
	failCall int
}

func (f *fakeRefs) git(stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = f.bare
	cmd.Env = append(gitenv.Without(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func (f *fakeRefs) graphQL(query string, vars map[string]any, out any) error {
	var data any
	switch {
	case strings.Contains(query, "viewer { login }"):
		data = map[string]any{"viewer": map[string]any{"login": "me"},
			"repository": map[string]any{"id": "R", "nameWithOwner": "o/r", "defaultBranchRef": map[string]any{"name": "main"}}}
	case strings.Contains(query, "search(query"):
		nodes := []any{}
		for head, n := range f.prs {
			oid, err := f.git("", "rev-parse", "refs/heads/"+head)
			if err != nil {
				return err
			}
			nodes = append(nodes, map[string]any{"id": fmt.Sprint("PR", n), "number": n, "headRefName": head,
				"headRefOid": oid, "baseRefName": f.bases[head], "author": map[string]any{"login": "me"}})
		}
		data = map[string]any{"search": map[string]any{"nodes": nodes}}
	case strings.Contains(query, "updateRefs"):
		f.calls = append(f.calls, nil)
		if len(f.calls) == f.failCall {
			return errors.New("GraphQL: something broke")
		}
		updates := vars["input"].(map[string]any)["refUpdates"].([]map[string]any)
		for _, u := range updates {
			f.calls[len(f.calls)-1] = append(f.calls[len(f.calls)-1], u["name"].(string))
		}
		for _, u := range updates {
			if _, err := f.git("", "update-ref", u["name"].(string), u["afterOid"].(string), u["beforeOid"].(string)); err != nil {
				return fmt.Errorf("GraphQL: %v", err)
			}
		}
	default:
		return errors.New("unexpected query: " + query)
	}
	if out == nil {
		return nil
	}
	encoded, _ := json.Marshal(data)
	return json.Unmarshal(encoded, out)
}

func (f *fakeRefs) rest(method, path string, body, out any) error {
	kind, ok := strings.CutPrefix(path, "repos/o/r/git/")
	if method != "POST" || !ok {
		return errors.New("unexpected call: " + method + " " + path)
	}
	var sha string
	var err error
	switch kind {
	case "blobs":
		content, _ := base64.StdEncoding.DecodeString(body.(map[string]string)["content"])
		sha, err = f.git(string(content), "hash-object", "-w", "--stdin")
	case "trees":
		var lines []string
		for _, e := range body.(map[string]any)["tree"].([]map[string]string) {
			lines = append(lines, fmt.Sprintf("%s %s %s\t%s", e["mode"], e["type"], e["sha"], e["path"]))
		}
		sha, err = f.git(strings.Join(lines, "\n")+"\n", "mktree")
	case "commits":
		c := body.(map[string]any)
		raw := "tree " + c["tree"].(string) + "\n"
		for _, p := range c["parents"].([]string) {
			raw += "parent " + p + "\n"
		}
		for _, role := range []string{"author", "committer"} {
			who := c[role].(map[string]string)
			at, perr := time.Parse(time.RFC3339, who["date"])
			if perr != nil {
				return perr
			}
			raw += fmt.Sprintf("%s %s <%s> %d %s\n", role, who["name"], who["email"], at.Unix(), at.Format("-0700"))
		}
		if sig, ok := c["signature"].(string); ok {
			raw += "gpgsig " + strings.ReplaceAll(sig, "\n", "\n ") + "\n"
		}
		sha, err = f.git(raw+"\n"+c["message"].(string), "hash-object", "-t", "commit", "-w", "--stdin")
	default:
		return errors.New("unexpected call: " + method + " " + path)
	}
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(map[string]string{"sha": sha})
	return json.Unmarshal(encoded, out)
}

// pushStack makes a checkout of o/r with a loop over a two-branch stack,
// lower (PR 1, on main) and upper (PR 2, on lower), which GitHub has as the
// loop began, then a fixup on lower with upper rebased onto it, so both
// branches have moved.
func pushStack(t *testing.T) (*fakeRefs, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv(relay.RelayEnv, "")
	t.Setenv(sessionEnv, "")
	dir, bare := t.TempDir(), filepath.Join(t.TempDir(), "github.git")
	write := func(name, content string) { os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644) }
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "init", "-q", "--bare", bare)
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	write("f", "base\n")
	gitIn(t, dir, "add", "f")
	gitIn(t, dir, "commit", "-qm", "Start")
	gitIn(t, dir, "switch", "-qc", "lower")
	write("a", "a\n")
	gitIn(t, dir, "add", "a")
	gitIn(t, dir, "commit", "-qm", "Add a")
	gitIn(t, dir, "switch", "-qc", "upper")
	write("c", "c\n")
	gitIn(t, dir, "add", "c")
	gitIn(t, dir, "commit", "-qm", "Add c")
	gitIn(t, dir, "push", "-q", bare, "main", "lower", "upper")
	gitIn(t, dir, "update-ref", "refs/remotes/origin/main", "main")
	useDir(t, dir)

	original := func(base, tip string) []Commit {
		t.Helper()
		c, err := commitsBetween(base, tip)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	state := &State{Repo: "o/r", Order: []string{"lower", "upper"}, Branches: map[string]*Branch{
		"lower": {PR: 1, Base: "main", RemoteOid: gitIn(t, dir, "rev-parse", "lower"), Commits: original("main", "lower")},
		"upper": {PR: 2, Base: "lower", RemoteOid: gitIn(t, dir, "rev-parse", "upper"), Commits: original("lower", "upper")},
	}}
	if err := state.save(); err != nil {
		t.Fatal(err)
	}

	oldLower := gitIn(t, dir, "rev-parse", "lower")
	gitIn(t, dir, "switch", "-q", "lower")
	write("a", "a fixed\n")
	gitIn(t, dir, "commit", "-qa", "--fixup=HEAD")
	gitIn(t, dir, "rebase", "-q", "--onto", "lower", oldLower, "upper")

	f := &fakeRefs{bare: bare, prs: map[string]int{"lower": 1, "upper": 2},
		bases: map[string]string{"lower": "main", "upper": "lower"}}
	oldGraphQL, oldREST := graphQL, objectsREST
	graphQL, objectsREST = f.graphQL, f.rest
	t.Cleanup(func() { graphQL, objectsREST = oldGraphQL, oldREST })
	return f, dir
}

// TestPush checks that Push moves a stack's branches one updateRefs call
// each, bottom up, as separate pushes would, so GitHub starts each pull
// request's workflows once.
func TestPush(t *testing.T) {
	t.Run("one ref per call, bottom up", func(t *testing.T) {
		f, dir := pushStack(t)
		if _, err := Push([]string{"upper", "lower"}, false); err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(f.calls); got != "[[refs/heads/lower] [refs/heads/upper]]" {
			t.Errorf("updateRefs calls: %s", got)
		}
		for _, b := range []string{"lower", "upper"} {
			if got, want := gitIn(t, f.bare, "rev-parse", b), gitIn(t, dir, "rev-parse", b); got != want {
				t.Errorf("GitHub has %s at %s, want %s", b, got, want)
			}
		}
	})

	// A call that fails leaves the branches below it recorded as pushed, so
	// the next push carries on from the branch that failed.
	t.Run("failure partway", func(t *testing.T) {
		f, dir := pushStack(t)
		oldUpper := gitIn(t, f.bare, "rev-parse", "upper")
		f.failCall = 2
		if _, err := Push(nil, false); err == nil {
			t.Fatal("a failed updateRefs call reported no error")
		}
		if got, want := gitIn(t, f.bare, "rev-parse", "lower"), gitIn(t, dir, "rev-parse", "lower"); got != want {
			t.Errorf("GitHub has lower at %s, want %s", got, want)
		}
		if got := gitIn(t, f.bare, "rev-parse", "upper"); got != oldUpper {
			t.Errorf("GitHub has upper at %s, want it unmoved at %s", got, oldUpper)
		}
		f.failCall = 0
		f.calls = nil
		if _, err := Push(nil, false); err != nil {
			t.Fatalf("pushing again after a failure: %v", err)
		}
		if got := fmt.Sprint(f.calls); got != "[[refs/heads/upper]]" {
			t.Errorf("updateRefs calls on pushing again: %s", got)
		}
		if got, want := gitIn(t, f.bare, "rev-parse", "upper"), gitIn(t, dir, "rev-parse", "upper"); got != want {
			t.Errorf("GitHub has upper at %s, want %s", got, want)
		}
	})
}
