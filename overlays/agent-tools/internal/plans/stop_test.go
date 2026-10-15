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

// stopRepo makes a repository whose origin/main is its first commit, with
// an unsigned commit after it if unsigned.
func stopRepo(t *testing.T, unsigned bool) string {
	t.Helper()
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
	if unsigned {
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "Add x")
	}
	return repo
}

// stopIn returns a function that runs Claude Code's Stop hook through c,
// and returns the reason it blocks the stop for, or nothing.
func stopIn(t *testing.T, c Config) func(session, cwd string, active bool) string {
	return func(session, cwd string, active bool) string {
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
}

func expectReason(t *testing.T, what, reason, want string) {
	t.Helper()
	if want == "" && reason != "" || !strings.Contains(reason, want) {
		t.Errorf("%s: reason %q, want %q", what, reason, want)
	}
}

func TestStopCheck(t *testing.T) {
	t.Run("nothing to sign", func(t *testing.T) {
		c := testConfig(t)
		repo := stopRepo(t, false)
		gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		expectReason(t, "nothing to sign", stopIn(t, c)("s1", repo, false), "")
	})

	t.Run("unsigned, no plan", func(t *testing.T) {
		c := testConfig(t)
		repo := stopRepo(t, true)
		gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		expectReason(t, "unsigned, no plan", stopIn(t, c)("s1", repo, false), "agent-plan new")
	})

	t.Run("stop hook active", func(t *testing.T) {
		c := testConfig(t)
		repo := stopRepo(t, true)
		gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		expectReason(t, "stop hook active", stopIn(t, c)("s1", repo, true), "")
	})

	t.Run("not json", func(t *testing.T) {
		c := testConfig(t)
		expectReason(t, "not json", string(c.StopCheck([]byte("x"), time.Now())), "")
	})

	t.Run("not a repository", func(t *testing.T) {
		c := testConfig(t)
		expectReason(t, "not a repository", stopIn(t, c)("s1", t.TempDir(), false), "")
	})

	t.Run("with a plan", testStopCheckWithAPlan)
}

// testStopCheckWithAPlan runs the Stop hook in a repository with an
// unsigned commit and a plan of session s2 for it, at each stage of the
// plan's life. Each case brings a plan of its own to its stage by the steps
// of every stage before it and its own.
func testStopCheckWithAPlan(t *testing.T) {
	type stage struct {
		name string
		// step brings a plan from the stage before to this one.
		step func(t *testing.T, p Plan)
		// session and other are the session the hook runs in, and whether it
		// runs in another repository.
		session string
		other   bool
		want    string
	}
	stages := []stage{
		{"planning", func(*testing.T, Plan) {}, "s1", false, ""},
		{"done, not verified", func(t *testing.T, p Plan) {
			setProp(t, p.File(), "STATUS", "working")
			p.Log(Event{Kind: "done", Item: "S1", Commit: "Add x"}, time.Now())
		}, "s1", false, "S1"},
		{"found by session", func(*testing.T, Plan) {}, "s2", true, "S1"},
		{"committed since the review", func(t *testing.T, p Plan) {
			p.Log(Event{Kind: "verified", Item: "S1", Evidence: "tests pass"}, time.Now())
			p.Log(Event{Kind: "reviewed", Item: p.Name}, time.Now().Add(-time.Hour))
		}, "s1", false, "review-change"},
		{"opted out", func(t *testing.T, p Plan) {
			setProp(t, p.File(), "PIPELINE", "opted-out:Wesley")
		}, "s1", false, ""},
		{"handed back", func(t *testing.T, p Plan) {
			setProp(t, p.File(), "PIPELINE", "full")
			p.Log(Event{Kind: "handed-back", Item: p.Name}, time.Now())
		}, "s1", false, ""},
		{"halted", func(t *testing.T, p Plan) {
			setProp(t, p.File(), "STATUS", "halted")
			p.Log(Event{Kind: "done", Item: "S1", Commit: "Add y"}, time.Now().Add(time.Hour))
		}, "s1", false, ""},
	}
	for i, s := range stages {
		t.Run(s.name, func(t *testing.T) {
			c := testConfig(t)
			repo := stopRepo(t, true)
			gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
			other := stopRepo(t, false)
			made, err := c.New(NewOptions{Title: "Add x", Slug: "x", Worktrees: []string{repo}, Sessions: []string{"claude:s2"},
				Harness: "claude"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			p, _ := c.Open(made.Plan)
			addItems(t, p.File(), "** Steps", "S1")
			for _, before := range stages[:i+1] {
				before.step(t, p)
			}
			cwd := repo
			if s.other {
				cwd = other
			}
			expectReason(t, s.name, stopIn(t, c)(s.session, cwd, false), s.want)
		})
	}
}

// TestStopCheckChat covers ECA's postRequest hook, which has no
// stop_hook_active: one block per chat, then the stop passes and the chat
// is checked again at its next stop.
func TestStopCheckChat(t *testing.T) {
	// stopIn returns a function that runs ECA's hook in repo for a chat,
	// keeping its state under state.
	stopIn := func(t *testing.T, c Config, repo, state string) func(chat string) string {
		return func(chat string) string {
			t.Helper()
			event, _ := json.Marshal(map[string]any{"hook_type": "postRequest", "hook_name": "x", "chat_id": chat,
				"cwd": repo, "workspaces": []string{repo}, "follow_up_active": false})
			return c.StopCheckChat(event, state, time.Now())
		}
	}

	t.Run("nothing to sign", func(t *testing.T) {
		stop := stopIn(t, testConfig(t), stopRepo(t, false), filepath.Join(t.TempDir(), "agent-tools", "stop"))
		if got := stop("c1"); got != "" {
			t.Fatalf("nothing to sign: %q", got)
		}
	})

	t.Run("one block per chat", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "agent-tools", "stop")
		stop := stopIn(t, testConfig(t), stopRepo(t, true), state)
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
	})

	t.Run("chat IDs that are no file names", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "agent-tools", "stop")
		stop := stopIn(t, testConfig(t), stopRepo(t, true), state)
		for _, chat := range []string{"c1", "", "../c3", "a/b"} {
			stop(chat)
		}
		entries, _ := os.ReadDir(state)
		for _, e := range entries {
			if strings.ContainsAny(e.Name(), "/\\") || strings.HasPrefix(e.Name(), ".") {
				t.Errorf("state file %q", e.Name())
			}
		}
	})

	t.Run("not json", func(t *testing.T) {
		c := testConfig(t)
		state := filepath.Join(t.TempDir(), "agent-tools", "stop")
		if got := c.StopCheckChat([]byte("x"), state, time.Now()); got != "" {
			t.Errorf("not json: %q", got)
		}
	})
}
