package loop

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeOpen answers what Open asks GitHub, for the viewer "me" in o/r with
// the default branch main: the refs and open pull requests viewRemote reads,
// from heads and prs, and createRef, createPullRequest and deleteRef, each
// recorded in calls. It is a fake because a test must not reach GitHub.
type fakeOpen struct {
	heads  map[string]string // branch to its tip on GitHub
	prs    map[string]int    // head branch to an open same-repository pull request
	failPR bool
	calls  []string
}

func (f *fakeOpen) graphQL(query string, vars map[string]any, out any) error {
	var data any
	switch {
	case strings.Contains(query, "viewer { login }"):
		data = map[string]any{"viewer": map[string]any{"login": "me"},
			"repository": map[string]any{"id": "R", "nameWithOwner": "o/r", "defaultBranchRef": map[string]any{"name": "main"}}}
	case strings.Contains(query, "headPRs:"):
		ref := func(name string) any {
			if oid, ok := f.heads[vars[name].(string)]; ok {
				return map[string]any{"target": map[string]any{"oid": oid}}
			}
			return nil
		}
		prs := func(name string) any {
			nodes := []any{}
			if n, ok := f.prs[vars[name].(string)]; ok {
				nodes = append(nodes, map[string]any{"number": n, "headRefName": vars[name], "author": map[string]any{"login": "me"}})
			}
			return map[string]any{"nodes": nodes}
		}
		data = map[string]any{"repository": map[string]any{"head": ref("head"), "base": ref("base"),
			"headPRs": prs("head"), "basePRs": prs("base")}}
	case strings.Contains(query, "createRef("):
		f.calls = append(f.calls, "createRef")
		data = map[string]any{"createRef": map[string]any{"ref": map[string]any{"id": "REF"}}}
	case strings.Contains(query, "createPullRequest("):
		f.calls = append(f.calls, "createPullRequest")
		if f.failPR {
			return errors.New("GraphQL: something broke")
		}
		data = map[string]any{"createPullRequest": map[string]any{"pullRequest": map[string]any{
			"number": 7, "url": "https://github.com/o/r/pull/7", "isDraft": true}}}
	case strings.Contains(query, "deleteRef("):
		f.calls = append(f.calls, "deleteRef")
	default:
		return errors.New("unexpected query: " + query)
	}
	if out == nil {
		return nil
	}
	encoded, _ := json.Marshal(data)
	return json.Unmarshal(encoded, out)
}

// pushedBranch makes a checkout of o/r, whose operator is t@e, with branch
// feat one commit "Add feat" past main, which GitHub has at that tip as the
// checkout last fetched it, with no pull request; it returns the fake and
// the checkout.
func pushedBranch(t *testing.T) (*fakeOpen, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv(ModeEnv, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	gitIn(t, dir, "config", "user.email", "t@e")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Start")
	gitIn(t, dir, "update-ref", "refs/remotes/origin/main", "main")
	gitIn(t, dir, "switch", "-qc", "feat")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Add feat")
	useDir(t, dir)
	f := &fakeOpen{heads: map[string]string{"main": gitIn(t, dir, "rev-parse", "main")}, prs: map[string]int{}}
	published(t, f, dir, "feat")
	old := graphQL
	graphQL = f.graphQL
	t.Cleanup(func() { graphQL = old })
	return f, dir
}

// published puts branch on GitHub at its local tip, as a push would.
func published(t *testing.T, f *fakeOpen, dir, branch string) {
	t.Helper()
	f.heads[branch] = gitIn(t, dir, "rev-parse", branch)
	gitIn(t, dir, "update-ref", "refs/remotes/origin/"+branch, branch)
	gitIn(t, dir, "config", "branch."+branch+".remote", "origin")
	gitIn(t, dir, "config", "branch."+branch+".merge", "refs/heads/"+branch)
}

// TestOpenPushed opens the draft pull request for a branch GitHub already
// has at the local tip, without creating or ever deleting that branch, and
// only when its commits are the operator's.
func TestOpenPushed(t *testing.T) {
	t.Run("refused/unsigned matching author", func(t *testing.T) {
		f, _ := pushedBranch(t)
		_, err := Open("feat", "", "Add feat", "Why it is needed.", false)
		var refusal Refusal
		if !errors.As(err, &refusal) || len(f.calls) != 0 {
			t.Fatalf("unsigned branch with matching author: error %v, mutations %v", err, f.calls)
		}
	})
	t.Run("opens it", func(t *testing.T) {
		f, _ := pushedOperatorBranch(t)
		result, err := Open("feat", "", "Add feat", "Why it is needed.", false)
		if err != nil {
			t.Fatal(err)
		}
		r := result.(map[string]any)
		if r["pr"] != 7 || r["draft"] != true || r["apiCalls"] != 1 {
			t.Errorf("result: %v", r)
		}
		if got := strings.Join(f.calls, " "); got != "createPullRequest" {
			t.Errorf("calls: %s", got)
		}
	})

	t.Run("dry run", func(t *testing.T) {
		f, _ := pushedOperatorBranch(t)
		result, err := Open("feat", "", "Add feat", "Why it is needed.", true)
		if err != nil {
			t.Fatal(err)
		}
		would := result.(map[string]any)["wouldOpen"].(map[string]any)
		if would["onGitHub"] != true || would["branch"] != "feat" || len(f.calls) != 0 {
			t.Errorf("dry run: %v, calls %v", result, f.calls)
		}
	})

	t.Run("a failed pull request leaves the branch", func(t *testing.T) {
		f, _ := pushedOperatorBranch(t)
		f.failPR = true
		if _, err := Open("feat", "", "Add feat", "Why it is needed.", false); err == nil || strings.Contains(err.Error(), "deleted") {
			t.Errorf("a failed pull request: %v", err)
		}
		if got := strings.Join(f.calls, " "); got != "createPullRequest" {
			t.Errorf("calls: %s", got)
		}
	})

	t.Run("its message is still checked", func(t *testing.T) {
		f, dir := pushedBranch(t)
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Add x.")
		published(t, f, dir, "feat")
		_, err := Open("feat", "", "Add feat", "Why it is needed.", false)
		if _, ok := err.(Refusal); !ok || !strings.Contains(err.Error(), "Add x.") || len(f.calls) != 0 {
			t.Errorf("a pushed commit ending in a period: %v, calls %v", err, f.calls)
		}
	})

	// Each case changes the branch or GitHub, and names what the refusal says.
	refused := map[string]struct {
		change func(t *testing.T, f *fakeOpen, dir string)
		says   string
	}{
		"GitHub's head is not the local tip": {func(t *testing.T, f *fakeOpen, dir string) {
			gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Add more")
		}, "at the local tip"},
		"unsigned commits despite matching author": {func(t *testing.T, f *fakeOpen, dir string) {
			gitIn(t, dir, "-c", "user.email=other@e", "commit", "-q", "--allow-empty", "-m", "Add theirs")
			published(t, f, dir, "feat")
		}, "no signature"},
		"an open pull request": {func(t *testing.T, f *fakeOpen, dir string) {
			f.prs["feat"] = 3
		}, "already the head of #3"},
	}
	for name, c := range refused {
		t.Run("refused/"+name, func(t *testing.T) {
			f, dir := pushedOperatorBranch(t)
			c.change(t, f, dir)
			_, err := Open("feat", "", "Add feat", "Why it is needed.", false)
			var policy Refusal
			if !errors.As(err, &policy) || !strings.Contains(err.Error(), c.says) || len(f.calls) != 0 {
				t.Errorf("%s: %v, calls %v", name, err, f.calls)
			}
		})
	}
}
