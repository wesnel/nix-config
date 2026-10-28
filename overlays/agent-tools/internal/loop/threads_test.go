package loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/record"
)

type fakeComment struct{ login, typ string }

type fakeThread struct {
	pr       int
	author   string // the PR's
	state    string
	resolved bool
	path     string
	comments []fakeComment
	// failPage makes every page of comments after the first fail.
	failPage bool
}

func (t *fakeThread) last() string { return t.comments[len(t.comments)-1].login }

// fakeGitHub answers the GraphQL that listing, replying to and resolving
// review threads make, for the viewer "me" in o/r, giving each thread's
// comments 100 to a page as GitHub does.
type fakeGitHub struct {
	threads   map[string]*fakeThread
	replies   map[string]string
	resolved  []string
	failReply string
}

// commentPage is the page of t's comments from start.
func commentPageOf(id string, t *fakeThread, start int) any {
	end := min(start+100, len(t.comments))
	nodes := []any{}
	for i, c := range t.comments[start:end] {
		nodes = append(nodes, map[string]any{"author": map[string]any{"login": c.login, "__typename": c.typ},
			"body": fmt.Sprintf("comment %d", start+i), "url": "https://x/" + id, "createdAt": "2026-10-20T10:00:00Z"})
	}
	return map[string]any{"totalCount": len(t.comments), "nodes": nodes,
		"pageInfo": map[string]any{"hasNextPage": end < len(t.comments), "endCursor": strconv.Itoa(end)}}
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
		f.threads[id].comments = append(f.threads[id].comments, fakeComment{"me", "User"})
		data = map[string]any{"addPullRequestReviewThreadReply": map[string]any{"comment": map[string]any{"url": "https://x/" + id}}}
	case strings.Contains(query, "resolveReviewThread"):
		id := vars["id"].(string)
		f.resolved = append(f.resolved, id)
		f.threads[id].resolved = true
	case strings.Contains(query, "reviewThreads(first: 50"):
		var ids []string
		for id, t := range f.threads {
			if t.pr == vars["number"].(int) {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		nodes := []any{}
		for _, id := range ids {
			t := f.threads[id]
			nodes = append(nodes, map[string]any{"id": id, "isResolved": t.resolved, "path": t.path, "line": 3,
				"comments": commentPageOf(id, t, 0)})
		}
		data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{
			"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}}}}
	case strings.Contains(query, "node(id: $id)"):
		id := vars["id"].(string)
		t := f.threads[id]
		if t == nil {
			return errors.New("GraphQL: Could not resolve to a node with the global id of '" + id + "'")
		}
		if after, ok := vars["after"].(string); ok {
			if t.failPage {
				return errors.New("GraphQL: something broke")
			}
			start, _ := strconv.Atoi(after)
			data = map[string]any{"node": map[string]any{"comments": commentPageOf(id, t, start)}}
			break
		}
		data = map[string]any{"node": map[string]any{
			"isResolved": t.resolved,
			"pullRequest": map[string]any{"number": t.pr, "state": t.state,
				"author": map[string]any{"login": t.author}, "repository": map[string]any{"nameWithOwner": "o/r"}},
			"comments": commentPageOf(id, t, 0)}}
	default:
		return errors.New("unexpected query: " + query)
	}
	if out == nil {
		return nil
	}
	encoded, _ := json.Marshal(data)
	return json.Unmarshal(encoded, out)
}

var (
	copilot = fakeComment{"copilot-pull-request-reviewer", "Bot"}
	me      = fakeComment{"me", "User"}
	josh    = fakeComment{"josh", "User"}
)

// fakeRepo makes a repository for o/r with one configured bot, copilot, and
// threads on PR 1 and PR 2, both by "me", in no loop mode, with review
// records kept in a directory of the test's own.
func fakeRepo(t *testing.T) *fakeGitHub {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv(ModeEnv, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	git.Dir = dir
	t.Cleanup(func() { git.Dir = "" })

	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, []byte(`{"bots": {"copilot": {"login": "^copilot", "request": {"reviewer": "@copilot"}}}}`), 0o644)
	t.Setenv("PR_LOOP_CONFIG", config)

	long := []fakeComment{copilot}
	for i := 1; i < 150; i++ {
		long = append(long, []fakeComment{copilot, me}[i%2])
	}
	mixed := append([]fakeComment{}, long...)
	mixed[120] = josh
	f := &fakeGitHub{replies: map[string]string{}, threads: map[string]*fakeThread{
		"bot1":      {pr: 1, author: "me", state: "OPEN", path: "a.go", comments: []fakeComment{copilot}},
		"answered1": {pr: 1, author: "me", state: "OPEN", path: "a.go", comments: []fakeComment{copilot, me}},
		"human1":    {pr: 1, author: "me", state: "OPEN", path: "b.go", comments: []fakeComment{josh}},
		"joined1":   {pr: 1, author: "me", state: "OPEN", path: "a.go", comments: []fakeComment{copilot, me, josh}},
		"posed1":    {pr: 1, author: "me", state: "OPEN", path: "a.go", comments: []fakeComment{{"copilot-impostor", "User"}}},
		"long1":     {pr: 1, author: "me", state: "OPEN", path: "c.go", comments: long},
		"longmix1":  {pr: 1, author: "me", state: "OPEN", path: "c.go", comments: mixed},
		"broken5":   {pr: 5, author: "me", state: "OPEN", path: "c.go", comments: long, failPage: true},
		"bot2":      {pr: 2, author: "me", state: "OPEN", path: "d.go", comments: []fakeComment{copilot}},
		"done2":     {pr: 2, author: "me", state: "OPEN", path: "d.go", comments: []fakeComment{copilot, me}, resolved: true},
		"human2":    {pr: 2, author: "me", state: "OPEN", path: "d.go", comments: []fakeComment{josh}},
		"theirs":    {pr: 3, author: "josh", state: "OPEN", comments: []fakeComment{copilot}},
		"closed":    {pr: 4, author: "me", state: "MERGED", comments: []fakeComment{copilot}},
	}}
	old := graphQL
	graphQL = f.graphQL
	t.Cleanup(func() { graphQL = old })
	return f
}

func refused(err error) bool {
	var r Refusal
	return errors.As(err, &r)
}

func TestReply(t *testing.T) {
	t.Run("outside a bots-mode loop", func(t *testing.T) {
		for _, mode := range []string{"", "people"} {
			f := fakeRepo(t)
			t.Setenv(ModeEnv, mode)
			if _, err := Reply("bot1", "x"); !refused(err) || !strings.Contains(err.Error(), "review-loop --harness claude|codex --bots|--people") {
				t.Errorf("mode %q: %v", mode, err)
			}
			if len(f.replies) != 0 {
				t.Errorf("mode %q posted %v", mode, f.replies)
			}
		}
	})

	for _, id := range []string{"bot1", "answered1", "long1", "bot2"} {
		t.Run("reply/"+id, func(t *testing.T) {
			f := fakeRepo(t)
			t.Setenv(ModeEnv, "bots")
			if _, err := Reply(id, "done"); err != nil || f.replies[id] != "done" {
				t.Errorf("reply to %s: %v", id, err)
			}
		})
	}

	// A person's comment anywhere in a thread, the 121st of 150 included,
	// makes it the operator's; so does a login that only looks like a bot's.
	for _, id := range []string{"human1", "joined1", "posed1", "longmix1", "theirs", "closed"} {
		t.Run("refused/"+id, func(t *testing.T) {
			f := fakeRepo(t)
			t.Setenv(ModeEnv, "bots")
			if _, err := Reply(id, "x"); !refused(err) {
				t.Errorf("reply to %s: %v", id, err)
			}
			if _, err := Resolve(id); !refused(err) {
				t.Errorf("resolve %s: %v", id, err)
			}
			if len(f.replies)+len(f.resolved) != 0 {
				t.Errorf("wrote %v %v", f.replies, f.resolved)
			}
		})
	}

	t.Run("a page that fails", func(t *testing.T) {
		f := fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		if _, err := Reply("broken5", "x"); err == nil || len(f.replies) != 0 {
			t.Errorf("a thread read in part was answered: %v", err)
		}
		if _, err := Threads(5, false); err == nil {
			t.Error("a thread read in part was listed")
		}
	})

	t.Run("resolve", func(t *testing.T) {
		f := fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		if _, err := Resolve("bot1"); !refused(err) {
			t.Errorf("resolved a thread whose last comment is the bot's: %v", err)
		}
		if _, err := Reply("bot1", "done"); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve("bot1"); err != nil || strings.Join(f.resolved, ",") != "bot1" {
			t.Errorf("resolving after a reply: %v, %v", err, f.resolved)
		}
	})

	t.Run("missing thread", func(t *testing.T) {
		fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		if _, err := Reply("missing", "x"); err == nil {
			t.Error("replied to a thread that does not exist")
		}
	})

	t.Run("empty reply", func(t *testing.T) {
		fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		if _, err := Reply("bot1", " \n"); !refused(err) {
			t.Errorf("empty reply: %v", err)
		}
	})
}

// threads lists the unresolved threads and writes every thread, with every
// comment and its author's type, to the PR's record, keeping drafts.
func TestThreads(t *testing.T) {
	fakeRepo(t)
	got, err := Threads(2, false)
	if err != nil {
		t.Fatal(err)
	}
	listed := got.([]Thread)
	if len(listed) != 2 || listed[0].ID != "bot2" || listed[0].Reviewer != "copilot" || listed[1].Reviewer != "human" ||
		listed[0].Comments[0].Type != "Bot" {
		t.Errorf("listed %+v", listed)
	}
	path, _ := record.PRFile("o", "r", 2)
	err = record.Update(path, func(p *record.PR) error {
		p.Threads[2].Draft = &record.Draft{Outcome: "rebut", Body: "No."}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Threads(2, true); err != nil {
		t.Fatal(err)
	}
	p, err := record.ReadPR(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Threads) != 3 || p.Threads[1].ID != "done2" || !p.Threads[1].Resolved || p.Threads[1].Comments[1].Author != "me" ||
		p.Threads[0].Comments[0].Type != "Bot" || p.Threads[0].Line != 3 || p.Threads[0].Path != "d.go" {
		t.Errorf("record %+v", p.Threads)
	}
	if d := p.Threads[2].Draft; d == nil || d.Body != "No." {
		t.Errorf("the draft was lost: %+v", p.Threads[2])
	}

	t.Run("every comment of a long thread", func(t *testing.T) {
		fakeRepo(t)
		listed, err := Threads(1, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, th := range listed.([]Thread) {
			if th.ID == "long1" && len(th.Comments) != 150 {
				t.Errorf("long1 has %d comments, want 150", len(th.Comments))
			}
		}
	})
}
