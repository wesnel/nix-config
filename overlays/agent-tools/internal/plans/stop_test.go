package plans

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setProp sets a property of a plan file's top heading.
func setProp(t *testing.T, file, name, value string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, ":"+name+":") {
			line = ":" + name + ": " + value
		}
		lines = append(lines, line)
	}
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStopCheck(t *testing.T) {
	c := testConfig(t)
	for _, v := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(v, "T")
	}
	for _, v := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(v, "t@e")
	}
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	other, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, other, "init", "-q", "-b", "main")
	gitIn(t, other, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, other, "update-ref", "refs/remotes/origin/main", "HEAD")

	stop := func(session, cwd string, active bool) string {
		t.Helper()
		event, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": session, "cwd": cwd, "stop_hook_active": active})
		out := c.StopCheck(event, time.Now())
		if len(out) == 0 {
			return ""
		}
		var got struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if err := json.Unmarshal(out, &got); err != nil || got.Decision != "block" || got.Reason == "" {
			t.Fatalf("output %s: %v", out, err)
		}
		return got.Reason
	}
	expect := func(what, reason, want string) {
		t.Helper()
		if want == "" && reason != "" || !strings.Contains(reason, want) {
			t.Errorf("%s: reason %q, want %q", what, reason, want)
		}
	}

	expect("nothing to sign", stop("s1", repo, false), "")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "Add x")
	expect("unsigned, no plan", stop("s1", repo, false), "agent-plan new")
	expect("stop hook active", stop("s1", repo, true), "")
	expect("not json", string(c.StopCheck([]byte("x"), time.Now())), "")
	expect("not a repository", stop("s1", t.TempDir(), false), "")

	made, err := c.New(NewOptions{Title: "Add x", Slug: "x", Worktrees: []string{repo}, Sessions: []string{"claude:s2"},
		Harness: "claude"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := c.Open(made.Plan)
	addItems(t, p.File(), "** Steps", "S1")
	expect("planning", stop("s1", repo, false), "")
	setProp(t, p.File(), "STATUS", "working")
	p.Log(Event{Kind: "done", Item: "S1", Commit: "Add x"}, time.Now())
	expect("done, not verified", stop("s1", repo, false), "S1")
	expect("found by session", stop("s2", other, false), "S1")
	p.Log(Event{Kind: "verified", Item: "S1", Evidence: "tests pass"}, time.Now())
	p.Log(Event{Kind: "reviewed", Item: p.Name}, time.Now().Add(-time.Hour))
	expect("committed since the review", stop("s1", repo, false), "review-change")
	setProp(t, p.File(), "PIPELINE", "opted-out:Wesley")
	expect("opted out", stop("s1", repo, false), "")
	setProp(t, p.File(), "PIPELINE", "full")
	p.Log(Event{Kind: "handed-back", Item: p.Name}, time.Now())
	expect("handed back", stop("s1", repo, false), "")
	setProp(t, p.File(), "STATUS", "halted")
	p.Log(Event{Kind: "done", Item: "S1", Commit: "Add y"}, time.Now().Add(time.Hour))
	expect("halted", stop("s1", repo, false), "")
}

// TestStopCheckChat covers ECA's postRequest hook, which has no
// stop_hook_active: one block per chat, then the stop passes and the chat
// is checked again at its next stop.
func TestStopCheckChat(t *testing.T) {
	c := testConfig(t)
	for _, v := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(v, "T")
	}
	for _, v := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(v, "t@e")
	}
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	state := filepath.Join(t.TempDir(), "agent-tools", "stop")

	stop := func(chat string) string {
		t.Helper()
		event, _ := json.Marshal(map[string]any{"hook_type": "postRequest", "hook_name": "x", "chat_id": chat,
			"cwd": repo, "workspaces": []string{repo}, "follow_up_active": false})
		return c.StopCheckChat(event, state, time.Now())
	}
	if got := stop("c1"); got != "" {
		t.Fatalf("nothing to sign: %q", got)
	}
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "Add x")
	if got := stop("c1"); !strings.Contains(got, "agent-plan new") {
		t.Fatalf("first stop: %q", got)
	}
	if _, err := os.Stat(filepath.Join(state, "c1")); err != nil {
		t.Errorf("no state file: %v", err)
	}
	if got := stop("c2"); !strings.Contains(got, "agent-plan new") {
		t.Errorf("another chat: %q", got)
	}
	if got := stop("c1"); got != "" {
		t.Errorf("second stop of the chat: %q", got)
	}
	if got := stop("c1"); !strings.Contains(got, "agent-plan new") {
		t.Errorf("stop after the pass: %q", got)
	}
	for _, chat := range []string{"", "../c3", "a/b"} {
		stop(chat)
	}
	entries, _ := os.ReadDir(state)
	for _, e := range entries {
		if strings.ContainsAny(e.Name(), "/\\") || strings.HasPrefix(e.Name(), ".") {
			t.Errorf("state file %q", e.Name())
		}
	}
	if got := c.StopCheckChat([]byte("x"), state, time.Now()); got != "" {
		t.Errorf("not json: %q", got)
	}
}
