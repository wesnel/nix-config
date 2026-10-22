package loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/relay"
)

type fakeStackPR struct {
	head, base, author, state string
	draft                     bool
}

// fakeStacks answers the GraphQL that reading stacks makes, and the REST
// calls that change them, for the viewer "me" in o/r, whose default branch
// is main. Like GitHub, it answers 422 to a write whose PRs are not each
// based on the one below.
type fakeStacks struct {
	prs       map[int]*fakeStackPR
	stacks    map[int][]int // stack number to its PRs, bottom first
	writes    []string
	conflicts int   // how many writes to answer with 409 first
	fail      error // the answer to the next write
}

// chains reports whether each of numbers is based on the one before.
func (f *fakeStacks) chains(numbers []int) bool {
	for i := 1; i < len(numbers); i++ {
		if f.prs[numbers[i]].base != f.prs[numbers[i-1]].head {
			return false
		}
	}
	return true
}

func (f *fakeStacks) rest(method, path string, body, out any) error {
	f.writes = append(f.writes, method+" "+path)
	if f.conflicts > 0 {
		f.conflicts--
		return &gh.StatusError{Code: 409}
	}
	if err := f.fail; err != nil {
		f.fail = nil
		return err
	}
	numbers := body.(map[string]any)["pull_requests"].([]int)
	number := 0
	switch rest, ok := strings.CutPrefix(path, "repos/o/r/stacks/"); {
	case method != "POST":
		return errors.New("unexpected method: " + method)
	case path == "repos/o/r/stacks":
		for _, n := range numbers {
			if f.stackOf(n) != 0 || !f.chains(numbers) {
				return &gh.StatusError{Code: 422}
			}
		}
		number = 20 + len(f.stacks)
		f.stacks[number] = numbers
	case ok && strings.HasSuffix(rest, "/add"):
		number, _ = strconv.Atoi(strings.TrimSuffix(rest, "/add"))
		members := f.stacks[number]
		if members == nil {
			return &gh.StatusError{Code: 404}
		}
		if !f.chains(append([]int{members[len(members)-1]}, numbers...)) {
			return &gh.StatusError{Code: 422}
		}
		f.stacks[number] = append(members, numbers...)
	default:
		return errors.New("unexpected path: " + path)
	}
	if out == nil {
		return nil
	}
	encoded, _ := json.Marshal(map[string]any{"number": number})
	return json.Unmarshal(encoded, out)
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
// 3 (c, on b) and 4 (d, on c), not stacked; 5 (e, on main) and 7 (f, on e),
// not stacked; 8 (g, on a); and 6 (x, on main) by josh.
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
			7: {head: "f", base: "e", author: "me", state: "OPEN"},
			8: {head: "g", base: "a", author: "me", state: "OPEN"},
		},
		stacks: map[int][]int{10: {1, 2}},
	}
	oldGraphQL, oldREST, oldSleep := graphQL, stacksREST, sleep
	graphQL, stacksREST, sleep = f.graphQL, f.rest, func(time.Duration) {}
	t.Cleanup(func() { graphQL, stacksREST, sleep = oldGraphQL, oldREST, oldSleep })
	return f
}

// registerPRs records a loop over the PRs numbered, as begin would.
func registerPRs(t *testing.T, f *fakeStacks, numbers ...int) {
	t.Helper()
	state := &State{Repo: "o/r", Branches: map[string]*Branch{}}
	for _, n := range numbers {
		pr := f.prs[n]
		state.Order = append(state.Order, pr.head)
		state.Branches[pr.head] = &Branch{PR: n, Base: pr.base}
	}
	if err := state.save(); err != nil {
		t.Fatal(err)
	}
}

// refusedWell reports whether err is a refusal that says what to do instead.
func refusedWell(err error) bool {
	return refused(err) && refusal.SaysInstead(err.Error())
}

func TestLink(t *testing.T) {
	f := stacksRepo(t)

	// Onto the top of an existing stack.
	got, err := Link([]string{"3", "d"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.stacks[10]) != "[1 2 3 4]" || fmt.Sprint(f.writes) != "[POST repos/o/r/stacks/10/add]" {
		t.Errorf("add: stack %v, writes %v", f.stacks[10], f.writes)
	}
	if r := got.(map[string]any); r["stack"] != 10 || fmt.Sprint(r["added"]) != "[3 4]" {
		t.Errorf("add: %v", r)
	}

	// Idempotent, for part or all of a stack.
	f.writes = nil
	for _, args := range [][]string{{"3", "4"}, {"1", "2", "3", "4"}, {"2"}} {
		got, err := Link(args)
		if err != nil || len(f.writes) != 0 {
			t.Errorf("again %v: %v, writes %v", args, err, f.writes)
		} else if r := got.(map[string]any); r["stack"] != 10 || fmt.Sprint(r["added"]) != "[]" {
			t.Errorf("again %v: %v", args, r)
		}
	}

	// A new stack, once GitHub stops answering 409.
	f.conflicts = 1
	if _, err := Link([]string{"5", "7"}); err != nil {
		t.Fatalf("after a 409: %v", err)
	}
	if s := f.stackOf(5); s == 0 || fmt.Sprint(f.stacks[s]) != "[5 7]" || len(f.writes) != 2 {
		t.Errorf("create: stacks %v, writes %v", f.stacks, f.writes)
	}
}

func TestLinkRefuses(t *testing.T) {
	f := stacksRepo(t)
	for name, args := range map[string][]string{
		"bases that do not chain":   {"5", "3"},
		"bottom first only":         {"7", "5"},
		"someone else's PR":         {"6"},
		"a PR that is not open":     {"99"},
		"a default branch":          {"main"},
		"a stack of one":            {"5"},
		"above a PR that has a top": {"8"},
	} {
		if _, err := Link(args); !refusedWell(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.writes) != 0 {
		t.Errorf("a refused link wrote: %v", f.writes)
	}

	// GitHub's own refusal is reported, and only a 409 is retried.
	f.fail = &gh.StatusError{Code: 422}
	if _, err := Link([]string{"5", "7"}); err == nil || refused(err) || len(f.writes) != 1 {
		t.Errorf("a 422: %v, writes %v", err, f.writes)
	}
	f.writes, f.conflicts = nil, 2
	if _, err := Link([]string{"5", "7"}); err == nil || len(f.writes) != 2 {
		t.Errorf("two 409s: %v, writes %v", err, f.writes)
	}
}

func TestLinkInALoop(t *testing.T) {
	for _, env := range []string{relay.RelayEnv, sessionEnv} {
		t.Run(env, func(t *testing.T) {
			f := stacksRepo(t)
			t.Setenv(env, "1")
			if _, err := Link([]string{"5", "7"}); !refusedWell(err) {
				t.Errorf("a session without a loop linked: %v", err)
			}
			registerPRs(t, f, 5)
			if _, err := Link([]string{"5", "7"}); !refusedWell(err) {
				t.Errorf("a session linked a PR outside its loop: %v", err)
			}
			if len(f.writes) != 0 {
				t.Errorf("writes: %v", f.writes)
			}
			registerPRs(t, f, 5, 7)
			if _, err := Link([]string{"5", "7"}); err != nil {
				t.Errorf("the loop's own PRs: %v", err)
			}
		})
	}
}

// A PR that open has just opened goes into its base's stack, and a failure
// to stack it leaves it open, saying how to stack it later.
func TestStackOpened(t *testing.T) {
	f := stacksRepo(t)
	repo := &Repo{Slug: "o/r", Default: "main", Viewer: "me"}
	opened := func(n int, head, base string) map[string]any {
		f.prs[n] = &fakeStackPR{head: head, base: base, author: "me", state: "OPEN", draft: true}
		return stackOpened(repo, head, base, n)
	}

	if got := opened(11, "h", "main"); len(got) != 0 || len(f.writes) != 0 {
		t.Errorf("on the default branch: %v, writes %v", got, f.writes)
	}
	if got := opened(12, "i", "b"); got["stack"] == nil || fmt.Sprint(f.stacks[10]) != "[1 2 12]" {
		t.Errorf("onto a stack's top: %v, stack %v", got, f.stacks[10])
	}
	if got := opened(13, "j", "f"); got["stack"] == nil || fmt.Sprint(f.stacks[f.stackOf(13)]) != "[7 13]" {
		t.Errorf("onto an unstacked PR: %v, stacks %v", got, f.stacks)
	}
	writes := len(f.writes)
	got := opened(14, "k", "a")
	reason, _ := got["unstacked"].(string)
	if !refusal.SaysInstead(reason) || !strings.Contains(reason, "`pr-loop link 1 14`") || len(f.writes) != writes {
		t.Errorf("onto a PR with another above it: %v, writes %v", got, f.writes[writes:])
	}

	t.Setenv(sessionEnv, "1")
	registerPRs(t, f, 13)
	got = opened(15, "l", "e")
	if reason, _ := got["unstacked"].(string); !strings.Contains(reason, "Wesley") || len(f.writes) != writes {
		t.Errorf("onto a PR outside the session's loop: %v, writes %v", got, f.writes[writes:])
	}
	if got := opened(16, "m", "j"); got["stack"] == nil || fmt.Sprint(f.stacks[f.stackOf(16)]) != "[7 13 16]" {
		t.Errorf("onto the session's own PR: %v, stacks %v", got, f.stacks)
	}
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
