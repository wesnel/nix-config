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
	threads  map[string]*fakeThread
	replies  map[string]string
	resolved []string
}

func (f *fakeGitHub) graphQL(query string, vars map[string]any, out any) error {
	var data any
	switch {
	case strings.Contains(query, "viewer { login }"):
		data = map[string]any{"viewer": map[string]any{"login": "me"},
			"repository": map[string]any{"id": "R", "nameWithOwner": "o/r", "defaultBranchRef": map[string]any{"name": "main"}}}
	case strings.Contains(query, "addPullRequestReviewThreadReply"):
		input := vars["input"].(map[string]any)
		id := input["pullRequestReviewThreadId"].(string)
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

func TestRepliesOutsideALoop(t *testing.T) {
	f := fakeRepo(t)
	for _, id := range []string{"human1", "bot1", "human2"} {
		if _, err := Reply(id, "done"); err != nil {
			t.Errorf("reply to %s: %v", id, err)
		}
	}
	if _, err := Resolve("human2"); err != nil {
		t.Errorf("resolving an answered human thread: %v", err)
	}
	if _, err := Resolve("human1"); err != nil {
		t.Errorf("resolving after a reply: %v", err)
	}
	f.threads["human2"].last = "josh"
	if _, err := Resolve("human2"); !refused(err) {
		t.Errorf("resolved a thread whose last comment is not the user's: %v", err)
	}
	for _, id := range []string{"theirs", "closed"} {
		if _, err := Reply(id, "x"); !refused(err) {
			t.Errorf("reply to %s: %v", id, err)
		}
	}
	if _, err := Reply("missing", "x"); err == nil {
		t.Error("replied to a thread that does not exist")
	}
	if _, err := Reply("human1", " \n"); !refused(err) {
		t.Errorf("empty reply: %v", err)
	}
}

func TestRepliesInALoop(t *testing.T) {
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

// A PR registered with begin keeps the loop's policy outside the session, so
// a second session cannot answer people on the loop's PRs.
func TestRegisteredPROutsideASession(t *testing.T) {
	fakeRepo(t)
	registerLoop(t)
	if _, err := Reply("human1", "x"); !refused(err) {
		t.Errorf("human thread on a loop's PR: %v", err)
	}
	if _, err := Reply("bot1", "x"); err != nil {
		t.Errorf("bot thread on a loop's PR: %v", err)
	}
	if _, err := Reply("human2", "x"); err != nil {
		t.Errorf("human thread on a PR outside the loop: %v", err)
	}
}
