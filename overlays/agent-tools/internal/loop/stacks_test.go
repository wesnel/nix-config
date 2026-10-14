package loop

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/relay"
)

type fakeStackPR struct {
	head, base, author, state string
	draft                     bool
}

// fakeStacks answers the GraphQL that reading stacks makes, for the viewer
// "me" in o/r, whose default branch is main.
type fakeStacks struct {
	prs    map[int]*fakeStackPR
	stacks map[int][]int // stack number to its PRs, bottom first
}

func (f *fakeStacks) stackOf(number int) int {
	for n, members := range f.stacks {
		for _, m := range members {
			if m == number {
				return n
			}
		}
	}
	return 0
}

func (f *fakeStacks) graphQL(query string, vars map[string]any, out any) error {
	var data any
	switch {
	case strings.Contains(query, "viewer { login }"):
		data = map[string]any{"viewer": map[string]any{"login": "me"},
			"repository": map[string]any{"id": "R", "nameWithOwner": "o/r", "defaultBranchRef": map[string]any{"name": "main"}}}
	case strings.Contains(query, "search(query"):
		nodes := []any{}
		for n, pr := range f.prs {
			if pr.author == "me" && pr.state == "OPEN" {
				nodes = append(nodes, map[string]any{"number": n, "headRefName": pr.head, "baseRefName": pr.base,
					"isDraft": pr.draft, "author": map[string]any{"login": pr.author}})
			}
		}
		data = map[string]any{"search": map[string]any{"nodes": nodes}}
	case strings.Contains(query, "stack {"):
		number := vars["number"].(int)
		if f.prs[number] == nil {
			return errors.New("GraphQL: Could not resolve to a PullRequest")
		}
		var stack any
		if s := f.stackOf(number); s != 0 {
			// Top first: GitHub's order is not relied on, only position.
			nodes := []any{}
			for i := len(f.stacks[s]) - 1; i >= 0; i-- {
				m := f.stacks[s][i]
				pr := f.prs[m]
				nodes = append(nodes, map[string]any{"position": i + 1, "pullRequest": map[string]any{"number": m,
					"headRefName": pr.head, "baseRefName": pr.base, "state": pr.state, "isDraft": pr.draft,
					"url": "https://github.com/o/r/pull/" + strconv.Itoa(m)}})
			}
			stack = map[string]any{"number": s, "baseRefName": f.prs[f.stacks[s][0]].base, "entries": map[string]any{"nodes": nodes}}
		}
		data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"stack": stack}}}
	default:
		return errors.New("unexpected query: " + query)
	}
	encoded, _ := json.Marshal(data)
	return json.Unmarshal(encoded, out)
}

// stacksRepo makes a checkout of o/r on branch b, whose PR is 2, with these
// open PRs by me: 1 (a, on main) and 2 (b, on a), stacked as GitHub stack 10;
// 3 (c, on b) and 4 (d, on c), not stacked; 5 (e, on main); and 6 (x, on
// main) by josh.
func stacksRepo(t *testing.T) *fakeStacks {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv(relay.RelayEnv, "")
	t.Setenv(sessionEnv, "")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, dir, "switch", "-qc", "b")
	git.Dir = dir
	t.Cleanup(func() { git.Dir = "" })

	f := &fakeStacks{
		prs: map[int]*fakeStackPR{
			1: {head: "a", base: "main", author: "me", state: "OPEN", draft: true},
			2: {head: "b", base: "a", author: "me", state: "OPEN", draft: true},
			3: {head: "c", base: "b", author: "me", state: "OPEN"},
			4: {head: "d", base: "c", author: "me", state: "OPEN"},
			5: {head: "e", base: "main", author: "me", state: "OPEN"},
			6: {head: "x", base: "main", author: "josh", state: "OPEN"},
		},
		stacks: map[int][]int{10: {1, 2}},
	}
	old := graphQL
	graphQL = f.graphQL
	t.Cleanup(func() { graphQL = old })
	return f
}

func TestStackShowsGitHubsRecord(t *testing.T) {
	stacksRepo(t)
	for _, arg := range []string{"", "b", "2", "#2", "a"} {
		got, err := Stack(arg)
		if err != nil {
			t.Fatalf("stack %q: %v", arg, err)
		}
		record := got.(map[string]any)["github"].(*GitHubStack)
		if record == nil || record.Number != 10 || len(record.PRs) != 2 {
			t.Fatalf("stack %q: %+v", arg, record)
		}
		want := []StackMember{
			{PR: 1, Branch: "a", Base: "main", State: "OPEN", Draft: true, URL: "https://github.com/o/r/pull/1"},
			{PR: 2, Branch: "b", Base: "a", State: "OPEN", Draft: true, URL: "https://github.com/o/r/pull/2"},
		}
		for i, m := range want {
			if record.PRs[i] != m {
				t.Errorf("stack %q, member %d: %+v, want %+v", arg, i, record.PRs[i], m)
			}
		}
	}
	got, err := Stack("c")
	if err != nil {
		t.Fatal(err)
	}
	if record := got.(map[string]any)["github"].(*GitHubStack); record != nil {
		t.Errorf("an unstacked PR has GitHub's record: %+v", record)
	}
	for _, arg := range []string{"6", "x", "99"} {
		if _, err := Stack(arg); !refused(err) || !refusal.SaysInstead(err.Error()) {
			t.Errorf("stack %q: %v", arg, err)
		}
	}
}
