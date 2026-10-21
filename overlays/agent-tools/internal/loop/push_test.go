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
)

// fakeRefs answers what Push asks GitHub, for the viewer "me" in o/r, from a
// bare repository standing in for GitHub's copy: the open PRs, with their
// heads read from it; Git objects, created in it and named by the SHA they
// hash to, as GitHub does; and updateRefs, which like GitHub moves each ref
// only from its beforeOid. It is a fake because a test must not reach
// GitHub.
type fakeRefs struct {
	bare    string
	prs     map[string]int // head branch to PR number
	bases   map[string]string
	drafts  map[string]bool
	authors map[string]string // a PR's author, "me" when unset
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
			author := f.authors[head]
			if author == "" {
				author = "me"
			}
			nodes = append(nodes, map[string]any{"id": fmt.Sprint("PR", n), "number": n, "headRefName": head,
				"headRefOid": oid, "baseRefName": f.bases[head], "isDraft": f.drafts[head], "author": map[string]any{"login": author}})
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

// pushStack makes a checkout of o/r over a two-branch stack, lower (PR 1,
// on main) and upper (PR 2, on lower), both ready, which GitHub has as last
// fetched; then a fixup on lower with upper rebased onto it, so both
// branches have moved, and upper no longer contains its head on GitHub. It
// returns the fake and the checkout, and upper's head on GitHub.
func pushStack(t *testing.T) (*fakeRefs, string, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv(ModeEnv, "")
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
	for _, b := range []string{"main", "lower", "upper"} {
		gitIn(t, dir, "update-ref", "refs/remotes/origin/"+b, b)
	}
	useDir(t, dir)

	oldLower, oldUpper := gitIn(t, dir, "rev-parse", "lower"), gitIn(t, dir, "rev-parse", "upper")
	gitIn(t, dir, "switch", "-q", "lower")
	write("a", "a fixed\n")
	gitIn(t, dir, "commit", "-qa", "--fixup=HEAD")
	gitIn(t, dir, "rebase", "-q", "--onto", "lower", oldLower, "upper")

	f := &fakeRefs{bare: bare, prs: map[string]int{"lower": 1, "upper": 2},
		bases: map[string]string{"lower": "main", "upper": "lower"}, drafts: map[string]bool{}, authors: map[string]string{}}
	oldGraphQL, oldREST := graphQL, objectsREST
	graphQL, objectsREST = f.graphQL, f.rest
	t.Cleanup(func() { graphQL, objectsREST = oldGraphQL, oldREST })
	return f, dir, oldUpper
}

// TestPush checks that Push moves a stack's branches one updateRefs call
// each, bottom up, as separate pushes would, so GitHub starts each pull
// request's workflows once, and only from the heads it expects.
func TestPush(t *testing.T) {
	onGitHub := func(t *testing.T, f *fakeRefs, dir, b string) bool {
		t.Helper()
		return gitIn(t, f.bare, "rev-parse", b) == gitIn(t, dir, "rev-parse", b)
	}

	t.Run("one ref per call, bottom up", func(t *testing.T) {
		f, dir, oldUpper := pushStack(t)
		if _, err := Push([]string{"upper", "lower"}, map[string]string{"upper": oldUpper}, false); err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(f.calls); got != "[[refs/heads/lower] [refs/heads/upper]]" {
			t.Errorf("updateRefs calls: %s", got)
		}
		for _, b := range []string{"lower", "upper"} {
			if !onGitHub(t, f, dir, b) {
				t.Errorf("GitHub has %s at %s", b, gitIn(t, f.bare, "rev-parse", b))
			}
			if got, want := gitIn(t, dir, "rev-parse", "refs/remotes/origin/"+b), gitIn(t, dir, "rev-parse", b); got != want {
				t.Errorf("origin/%s is at %s, want %s", b, got, want)
			}
		}
	})

	t.Run("HEAD's branch alone by default", func(t *testing.T) {
		f, dir, _ := pushStack(t)
		gitIn(t, dir, "switch", "-q", "lower")
		if _, err := Push(nil, nil, false); err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(f.calls); got != "[[refs/heads/lower]]" {
			t.Errorf("updateRefs calls: %s", got)
		}
	})

	// A call that fails leaves the branches below it pushed, with their
	// tracking refs moved, so the next push carries on from the branch that
	// failed.
	t.Run("failure partway", func(t *testing.T) {
		f, dir, oldUpper := pushStack(t)
		f.failCall = 2
		leases := map[string]string{"upper": oldUpper}
		if _, err := Push([]string{"lower", "upper"}, leases, false); err == nil {
			t.Fatal("a failed updateRefs call reported no error")
		}
		if !onGitHub(t, f, dir, "lower") {
			t.Error("lower was not pushed")
		}
		if got := gitIn(t, f.bare, "rev-parse", "upper"); got != oldUpper {
			t.Errorf("GitHub has upper at %s, want it unmoved at %s", got, oldUpper)
		}
		f.failCall, f.calls = 0, nil
		if _, err := Push([]string{"lower", "upper"}, leases, false); err != nil {
			t.Fatalf("pushing again after a failure: %v", err)
		}
		if got := fmt.Sprint(f.calls); got != "[[refs/heads/upper]]" || !onGitHub(t, f, dir, "upper") {
			t.Errorf("updateRefs calls on pushing again: %s", got)
		}
	})

	t.Run("a rebased branch needs a lease", func(t *testing.T) {
		f, dir, oldUpper := pushStack(t)
		if _, err := Push([]string{"lower", "upper"}, nil, false); !refusedWell(err) || !strings.Contains(err.Error(), "--lease upper=") {
			t.Errorf("a rebased branch without a lease: %v", err)
		}
		if len(f.calls) != 0 {
			t.Errorf("pushed: %v", f.calls)
		}
		if _, err := Push([]string{"upper"}, map[string]string{"upper": oldUpper}, false); err != nil || !onGitHub(t, f, dir, "upper") {
			t.Errorf("with a lease: %v", err)
		}
	})

	// Someone pushed to GitHub since the last fetch: nothing moves, with a
	// lease or without.
	t.Run("the remote moved", func(t *testing.T) {
		f, dir, oldUpper := pushStack(t)
		gitIn(t, dir, "push", "-q", f.bare, "lower:refs/heads/other")
		gitIn(t, f.bare, "update-ref", "refs/heads/upper", "refs/heads/other")
		_, err := Push([]string{"upper"}, map[string]string{"upper": oldUpper}, false)
		if !refusedWell(err) || !strings.Contains(err.Error(), "someone else pushed") || len(f.calls) != 0 {
			t.Errorf("a moved remote: %v, %v", err, f.calls)
		}
	})

	t.Run("a plain commit on a ready PR", func(t *testing.T) {
		f, dir, _ := pushStack(t)
		gitIn(t, dir, "switch", "-q", "lower")
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Add more")
		if _, err := Push([]string{"lower"}, nil, false); !refusedWell(err) || len(f.calls) != 0 {
			t.Errorf("a plain commit on a ready PR: %v, %v", err, f.calls)
		}
		f.drafts["lower"] = true
		if _, err := Push([]string{"lower"}, nil, false); err != nil || !onGitHub(t, f, dir, "lower") {
			t.Errorf("a plain commit on a draft: %v", err)
		}
	})

	// An amend keeps the message, so the lease the first refusal names
	// must not carry the changed commit onto a ready PR.
	// Whitespace counts: an indent is a change in Python, YAML or a Makefile.
	for name, content := range map[string]string{"content": "a rewritten\n", "whitespace alone": "    a\n"} {
		t.Run("an amended own commit, with a lease/"+name, func(t *testing.T) {
			f, dir, _ := pushStack(t)
			gitIn(t, dir, "switch", "-q", "lower")
			gitIn(t, dir, "reset", "-q", "--hard", "refs/remotes/origin/lower")
			old := gitIn(t, dir, "rev-parse", "lower")
			os.WriteFile(filepath.Join(dir, "a"), []byte(content), 0o644)
			gitIn(t, dir, "commit", "-qa", "--amend", "--no-edit")
			if _, err := Push([]string{"lower"}, nil, false); !refusedWell(err) || !strings.Contains(err.Error(), "--lease lower=") {
				t.Errorf("without a lease: %v", err)
			}
			_, err := Push([]string{"lower"}, map[string]string{"lower": old}, false)
			if !refusedWell(err) || !strings.Contains(err.Error(), "--fixup=<commit>") || len(f.calls) != 0 {
				t.Errorf("an amended own commit on a ready PR: %v, %v", err, f.calls)
			}
			if got := gitIn(t, f.bare, "rev-parse", "lower"); got != old {
				t.Errorf("GitHub's lower moved to %s", got)
			}
		})
	}

	for name, edit := range map[string]func(f *fakeRefs){
		"a default branch":   func(f *fakeRefs) { f.prs["main"] = 3 },
		"another user's PR":  func(f *fakeRefs) { f.authors["lower"] = "josh" },
		"a branch with none": func(f *fakeRefs) { delete(f.prs, "lower") },
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			f, _, _ := pushStack(t)
			edit(f)
			branch := "lower"
			if name == "a default branch" {
				branch = "main"
			}
			if _, err := Push([]string{branch}, nil, false); !refused(err) || len(f.calls) != 0 {
				t.Errorf("%s: %v, %v", name, err, f.calls)
			}
		})
	}

	t.Run("people mode", func(t *testing.T) {
		f, _, _ := pushStack(t)
		t.Setenv(ModeEnv, "people")
		if _, err := Push([]string{"lower"}, nil, false); !refusedWell(err) || !strings.Contains(err.Error(), "git sign-pass --push") || len(f.calls) != 0 {
			t.Errorf("people mode: %v", err)
		}
	})
}

// checkPatches compares a merge by its resolution, and an empty change
// matches only the same commit.
func TestCheckPatches(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	write := func(content string) { os.WriteFile(filepath.Join(dir, "f"), []byte(content), 0o644) }
	gitIn(t, dir, "init", "-q", "-b", "main")
	write("base\n")
	gitIn(t, dir, "add", "f")
	gitIn(t, dir, "commit", "-qm", "Start")
	base := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "switch", "-qc", "side")
	write("side\n")
	gitIn(t, dir, "commit", "-qam", "Side")
	gitIn(t, dir, "switch", "-qc", "pr", "main")
	write("pr\n")
	gitIn(t, dir, "commit", "-qam", "PR")
	gitIn(t, dir, "merge", "-q", "-X", "ours", "-m", "Merge side", "side")
	write("resolved one\n")
	gitIn(t, dir, "commit", "-qa", "--amend", "--no-edit")
	merged := gitIn(t, dir, "rev-parse", "HEAD")
	write("resolved two\n")
	gitIn(t, dir, "commit", "-qa", "--amend", "--no-edit")
	remerged := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Note")
	empty := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "commit", "-q", "--amend", "--allow-empty", "--no-edit", "--date=2001-01-01T00:00:00")
	reempty := gitIn(t, dir, "rev-parse", "HEAD")
	// A change outside the directory the command runs in, where
	// diff.relative is set.
	gitIn(t, dir, "switch", "-qc", "rel", "main")
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "s"), []byte("s\n"), 0o644)
	write("outside\n")
	gitIn(t, dir, "add", "f", "sub/s")
	gitIn(t, dir, "commit", "-qm", "Rel")
	relative := gitIn(t, dir, "rev-parse", "HEAD")
	write("outside, changed\n")
	gitIn(t, dir, "commit", "-qa", "--amend", "--no-edit")
	rerelative := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "config", "diff.relative", "true")
	// A submodule the checked-out .gitmodules says to ignore, re-pointed.
	gitIn(t, dir, "switch", "-qc", "sm", "main")
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitmodules"), []byte("[submodule \"mod\"]\n\tpath = mod\n\turl = ./x\n\tignore = all\n"), 0o644)
	gitIn(t, dir, "add", ".gitmodules")
	gitIn(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+base+",mod")
	gitIn(t, dir, "commit", "-qm", "Add mod")
	pointed := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "update-index", "--cacheinfo", "160000,"+merged+",mod")
	gitIn(t, dir, "commit", "-q", "--amend", "--no-edit")
	repointed := gitIn(t, dir, "rev-parse", "HEAD")
	useDir(t, dir)

	for _, c := range []struct {
		name        string
		before, now string
		refuse      bool
	}{
		{"the same merge", merged, merged, false},
		{"a merge resolved again", merged, remerged, true},
		{"the same empty commit", empty, empty, false},
		{"an empty commit remade", empty, reempty, true},
		{"an ignored submodule re-pointed", pointed, repointed, true},
	} {
		err := checkPatches("pr", base, c.before, base, c.now)
		if c.refuse != (err != nil) || err != nil && !refusedWell(err) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	useDir(t, filepath.Join(dir, "sub"))
	if err := checkPatches("rel", base, relative, base, rerelative); !refusedWell(err) {
		t.Errorf("a change outside the directory, with diff.relative: %v", err)
	}
}
