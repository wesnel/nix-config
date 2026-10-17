package loop

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/relay"
)

type fakeThread struct {
	pr       int
	head     string
	author   string // the PR's
	state    string
	starter  string
	last     string
	resolved bool
}

// fakeGitHub answers the GraphQL that replying and resolving make, for the
// viewer "me" in o/r.
type fakeGitHub struct {
	threads   map[string]*fakeThread
	replies   map[string]string
	resolved  []string
	failReply string
}

func (f *fakeGitHub) graphQL(query string, vars map[string]any, out any) error {
	var data any
	switch {
	case strings.Contains(query, "reviewThreads(first: 100"):
		nodes := []any{}
		for id, t := range f.threads {
			if t.pr == vars["number"] {
				nodes = append(nodes, map[string]any{"id": id})
			}
		}
		data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": false}, "nodes": nodes}}}}
	case strings.Contains(query, "viewer { login }"):
		data = map[string]any{"viewer": map[string]any{"login": "me"},
			"repository": map[string]any{"id": "R", "nameWithOwner": "o/r", "defaultBranchRef": map[string]any{"name": "main"}}}
	case strings.Contains(query, "addPullRequestReviewThreadReply"):
		input := vars["input"].(map[string]any)
		id := input["pullRequestReviewThreadId"].(string)
		if id == f.failReply {
			return errors.New("GraphQL: something broke")
		}
		f.replies[id] = input["body"].(string)
		f.threads[id].last = "me"
		data = map[string]any{"addPullRequestReviewThreadReply": map[string]any{"comment": map[string]any{"url": "https://x/" + id}}}
	case strings.Contains(query, "resolveReviewThread"):
		id := vars["id"].(string)
		f.resolved = append(f.resolved, id)
		f.threads[id].resolved = true
	case strings.Contains(query, "node(id: $id)"):
		t := f.threads[vars["id"].(string)]
		if t == nil {
			return errors.New("GraphQL: Could not resolve to a node with the global id of '" + vars["id"].(string) + "'")
		}
		author := func(login string) any {
			return map[string]any{"nodes": []any{map[string]any{"author": map[string]any{"login": login}}}}
		}
		data = map[string]any{"node": map[string]any{
			"isResolved": t.resolved,
			"pullRequest": map[string]any{"number": t.pr, "state": t.state, "headRefName": t.head,
				"author": map[string]any{"login": t.author}, "repository": map[string]any{"nameWithOwner": "o/r"}},
			"first": author(t.starter), "last": author(t.last)}}
	default:
		return errors.New("unexpected query: " + query)
	}
	if out == nil {
		return nil
	}
	encoded, _ := json.Marshal(data)
	return json.Unmarshal(encoded, out)
}

// fakeRepo makes a repository for o/r with one configured bot, copilot, and
// threads on PR 1 (branch feat) and PR 2 (branch other), both by "me".
func fakeRepo(t *testing.T) *fakeGitHub {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv(relay.RelayEnv, "")
	t.Setenv(sessionEnv, "")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	git.Dir = dir
	t.Cleanup(func() { git.Dir = "" })

	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, []byte(`{"bots": {"copilot": {"login": "^copilot", "request": {"reviewer": "@copilot"}}}}`), 0o644)
	t.Setenv("PR_LOOP_CONFIG", config)

	f := &fakeGitHub{replies: map[string]string{}, threads: map[string]*fakeThread{
		"human1":   {pr: 1, head: "feat", author: "me", state: "OPEN", starter: "josh", last: "josh"},
		"bot1":     {pr: 1, head: "feat", author: "me", state: "OPEN", starter: "copilot-pull-request-reviewer", last: "copilot-pull-request-reviewer"},
		"human2":   {pr: 2, head: "other", author: "me", state: "OPEN", starter: "josh", last: "josh"},
		"bot2":     {pr: 2, head: "other", author: "me", state: "OPEN", starter: "copilot-pull-request-reviewer", last: "copilot-pull-request-reviewer"},
		"bot3":     {pr: 2, head: "other", author: "me", state: "OPEN", starter: "copilot-pull-request-reviewer", last: "copilot-pull-request-reviewer"},
		"done2":    {pr: 2, head: "other", author: "me", state: "OPEN", starter: "copilot-pull-request-reviewer", last: "me", resolved: true},
		"theirs":   {pr: 3, head: "x", author: "josh", state: "OPEN", starter: "me", last: "me"},
		"closed":   {pr: 4, head: "y", author: "me", state: "MERGED", starter: "josh", last: "josh"},
		"answered": {pr: 2, head: "other", author: "me", state: "OPEN", starter: "josh", last: "me"},
	}}
	old := graphQL
	graphQL = f.graphQL
	t.Cleanup(func() { graphQL = old })
	return f
}

// registerLoop records a loop over PR 1 alone, as begin would.
func registerLoop(t *testing.T) {
	t.Helper()
	state := &State{Repo: "o/r", Order: []string{"feat"}, Branches: map[string]*Branch{"feat": {PR: 1, Base: "main"}}}
	if err := state.save(); err != nil {
		t.Fatal(err)
	}
}

func refused(err error) bool {
	var r Refusal
	return errors.As(err, &r)
}

func TestReply(t *testing.T) {
	t.Run("outside a loop", testReplyOutsideALoop)
	t.Run("in a loop", testReplyInALoop)
	t.Run("registered PR outside a session", testReplyRegisteredPROutsideASession)
}

func testReplyOutsideALoop(t *testing.T) {
	for _, id := range []string{"bot1", "bot2"} {
		t.Run("reply/"+id, func(t *testing.T) {
			fakeRepo(t)
			if _, err := Reply(id, "done"); err != nil {
				t.Errorf("reply to %s: %v", id, err)
			}
		})
	}

	t.Run("resolve", func(t *testing.T) {
		f := fakeRepo(t)
		for _, id := range []string{"bot1", "bot2"} {
			if _, err := Reply(id, "done"); err != nil {
				t.Fatalf("reply to %s: %v", id, err)
			}
		}
		for _, id := range []string{"bot1", "bot2"} {
			if _, err := Resolve(id); err != nil {
				t.Errorf("resolving %s after a reply: %v", id, err)
			}
		}
		f.threads["bot2"].last = "copilot-pull-request-reviewer"
		if _, err := Resolve("bot2"); !refused(err) {
			t.Errorf("resolved a thread whose last comment is not the user's: %v", err)
		}
	})

	for _, id := range []string{"human1", "human2", "answered"} {
		t.Run("refused person/"+id, func(t *testing.T) {
			f := fakeRepo(t)
			if _, err := Reply(id, "x"); !refused(err) {
				t.Errorf("reply to %s: %v", id, err)
			}
			if _, err := Resolve(id); !refused(err) {
				t.Errorf("resolve %s: %v", id, err)
			}
			if len(f.replies) != 0 {
				t.Errorf("replied to people: %v", f.replies)
			}
		})
	}

	for _, id := range []string{"theirs", "closed"} {
		t.Run("refused/"+id, func(t *testing.T) {
			fakeRepo(t)
			if _, err := Reply(id, "x"); !refused(err) {
				t.Errorf("reply to %s: %v", id, err)
			}
		})
	}

	t.Run("missing thread", func(t *testing.T) {
		fakeRepo(t)
		if _, err := Reply("missing", "x"); err == nil {
			t.Error("replied to a thread that does not exist")
		}
	})

	t.Run("empty reply", func(t *testing.T) {
		f := fakeRepo(t)
		if _, err := Reply("bot1", " \n"); !refused(err) || !strings.Contains(err.Error(), "empty") {
			t.Errorf("empty reply: %v", err)
		}
		if len(f.replies) != 0 {
			t.Errorf("posted an empty reply: %v", f.replies)
		}
	})
}

func testReplyInALoop(t *testing.T) {
	for _, env := range []string{relay.RelayEnv, sessionEnv} {
		t.Run(env, func(t *testing.T) {
			f := fakeRepo(t)
			t.Setenv(env, "1")
			if _, err := Reply("bot1", "x"); !refused(err) || !strings.Contains(err.Error(), "begin") {
				t.Errorf("a session without a loop replied: %v", err)
			}
			registerLoop(t)
			if _, err := Reply("bot1", "fixed"); err != nil {
				t.Errorf("reply to a bot in the loop: %v", err)
			}
			for _, id := range []string{"human1", "human2", "answered"} {
				if _, err := Reply(id, "x"); !refused(err) {
					t.Errorf("reply to %s: %v", id, err)
				}
				if _, err := Resolve(id); !refused(err) {
					t.Errorf("resolve %s: %v", id, err)
				}
			}
			if _, ok := f.replies["human1"]; ok {
				t.Error("a loop replied to a person")
			}
		})
	}
}

// A PR registered with begin keeps the loop's policy outside the session, and
// people's threads stay refused on PRs outside the loop too.
func testReplyRegisteredPROutsideASession(t *testing.T) {
	fakeRepo(t)
	registerLoop(t)
	if _, err := Reply("human1", "x"); !refused(err) {
		t.Errorf("human thread on a loop's PR: %v", err)
	}
	if _, err := Reply("bot1", "x"); err != nil {
		t.Errorf("bot thread on a loop's PR: %v", err)
	}
	if _, err := Reply("human2", "x"); !refused(err) {
		t.Errorf("human thread on a PR outside the loop: %v", err)
	}
	if _, err := Reply("bot2", "x"); err != nil {
		t.Errorf("bot thread on a PR outside the loop: %v", err)
	}
}
