package plans

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/routes"
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
// an unsigned commit after it if unsigned, and gives the test a state
// directory of its own.
func stopRepo(t *testing.T, unsigned bool) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
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
		commitFile(t, repo, "x", "Add x")
	}
	return repo
}

// commitFile commits a new file name in repo with message.
func commitFile(t *testing.T, repo, name, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", name)
	gitIn(t, repo, "commit", "-q", "-m", message)
}

// patchID is git's patch ID of the change origin/main...HEAD in repo, by
// git's own pipeline.
func patchID(t *testing.T, repo string) string {
	t.Helper()
	return patchIDOf(t, repo, "origin/main")
}

// patchIDOf is git's patch ID of the change base...HEAD in repo.
func patchIDOf(t *testing.T, repo, base string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", "git diff "+base+"...HEAD | git patch-id --stable")
	cmd.Dir, cmd.Env = repo, gitenv.Without(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	id, _, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	return id
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

	// A session that only read or answered a question owes nothing for
	// the unsigned commits it finds.
	t.Run("others' unsigned commits", func(t *testing.T) {
		c := testConfig(t)
		repo := stopRepo(t, true)
		gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		if _, _, err := c.RecordCommit("s2", repo, Event{Commit: "Add x", Summary: "Add x"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		expectReason(t, "others' unsigned commits", stopIn(t, c)("s1", repo, false), "")
	})

	// The plan a session's first commit made for it owes that commit's
	// review.
	t.Run("this session's commit, no plan before it", func(t *testing.T) {
		c := testConfig(t)
		repo := stopRepo(t, true)
		gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		if _, _, err := c.RecordCommit("s1", repo, Event{Commit: "Add x", Summary: "Add x"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		expectReason(t, "this session's commit", stopIn(t, c)("s1", repo, false),
			"no review receipt matches the change origin/main..HEAD")
	})

	// PIPELINE is what the log gives it: edited in the file alone, it is
	// drift, and opts nothing out.
	t.Run("opted out", func(t *testing.T) {
		c := testConfig(t)
		repo := stopRepo(t, true)
		gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		p, _, err := c.RecordCommit("s1", repo, Event{Commit: "Add x", Summary: "Add x"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		setProp(t, p.File(), "PIPELINE", "opted-out:by-hand")
		expectReason(t, "in the file", stopIn(t, c)("s1", repo, false), "no review receipt matches")
		if err := p.SetProperty("", "PIPELINE", []string{"opted-out:operator-asked"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		expectReason(t, "logged", stopIn(t, c)("s1", repo, false), "")
	})

	// A routed change owes a route receipt as well as the general one.
	t.Run("route owed", func(t *testing.T) {
		c := testConfig(t)
		repo := stopRepo(t, true)
		gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		p, _, err := c.RecordCommit("s1", repo, Event{Commit: "Add x", Summary: "Add x"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		head := strings.TrimSpace(gitIn(t, repo, "rev-parse", "HEAD"))
		p.Receipt("", Receipt{Range: "origin/main..HEAD", PatchID: patchID(t, repo)}, time.Now())
		p.Routed("", Routed{Branch: "main", Base: strings.TrimSpace(gitIn(t, repo, "rev-parse", "origin/main")), Tip: head,
			Routes: map[string][]string{"security": {head}}}, time.Now())
		expectReason(t, "routed", stopIn(t, c)("s1", repo, false), "git fresh-review --focus security")
		p.Receipt("", Receipt{Range: "origin/main..HEAD", Tip: head, PatchID: patchID(t, repo), Focus: "security", Branch: "main"},
			time.Now())
		expectReason(t, "reviewed", stopIn(t, c)("s1", repo, false), "")
	})

	// The command named is for a route with commits uncovered, not one
	// whose receipt only has findings to settle.
	t.Run("the route uncovered", func(t *testing.T) {
		known := routes.Known
		routes.Known = append(slices.Clone(known), "compat")
		t.Cleanup(func() { routes.Known = known })
		c := testConfig(t)
		repo := stopRepo(t, true)
		gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		p, _, err := c.RecordCommit("s1", repo, Event{Commit: "Add x", Summary: "Add x"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		head := strings.TrimSpace(gitIn(t, repo, "rev-parse", "HEAD"))
		p.Receipt("", Receipt{Range: "origin/main..HEAD", PatchID: patchID(t, repo)}, time.Now())
		p.Routed("", Routed{Branch: "main", Base: strings.TrimSpace(gitIn(t, repo, "rev-parse", "origin/main")), Tip: head,
			Routes: map[string][]string{"compat": {head}, "security": {head}}}, time.Now())
		p.Receipt("", Receipt{Range: "origin/main..HEAD", Tip: head, PatchID: patchID(t, repo), Focus: "compat", Branch: "main",
			FindingIDs: []string{"compat-1"}, Findings: 1}, time.Now())
		reason := stopIn(t, c)("s1", repo, false)
		if !strings.Contains(reason, "--focus security") || !strings.Contains(reason, "--route security") ||
			strings.Contains(reason, "--focus compat") {
			t.Errorf("reason %q", reason)
		}
	})

	t.Run("stop hook active", func(t *testing.T) {
		c := testConfig(t)
		repo := stopRepo(t, true)
		gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		if _, _, err := c.RecordCommit("s1", repo, Event{Commit: "Add x", Summary: "Add x"}, time.Now()); err != nil {
			t.Fatal(err)
		}
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

	// The session's commits, and its receipts, go where Locate puts them:
	// the plan of the worktree, though another plan names the session.
	t.Run("committed to the worktree's plan, not its own", func(t *testing.T) {
		c := testConfig(t)
		repo := stopRepo(t, true)
		gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		own, err := c.New(NewOptions{Title: "Mine", Slug: "mine", Sessions: []string{"claude:s1"}, Harness: "claude"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		theirs, err := c.New(NewOptions{Title: "Theirs", Slug: "theirs", Worktrees: []string{repo}, Sessions: []string{"claude:s9"},
			Harness: "claude"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{own.Plan, theirs.Plan} {
			setProp(t, file, "STATUS", "working")
		}
		p, _, ok := c.Locate("s1", repo)
		if !ok || p.File() != theirs.Plan {
			t.Fatalf("Locate found %s, want %s", p.File(), theirs.Plan)
		}
		p.Record("", Event{Kind: "committed", Commit: "Add x", Session: "s1"}, time.Now())
		expectReason(t, "unreviewed", stopIn(t, c)("s1", repo, false), "no review receipt matches the change origin/main..HEAD")
		// As WriteReceipt logs it, in the plan Locate finds.
		if _, err := p.Receipt("", Receipt{Range: "origin/main..HEAD", PatchID: patchID(t, repo)}, time.Now()); err != nil {
			t.Fatal(err)
		}
		expectReason(t, "reviewed", stopIn(t, c)("s1", repo, false), "")
	})

	// Without a remote default branch, a receipt still has to match.
	t.Run("no origin", func(t *testing.T) {
		c := testConfig(t)
		repo := stopRepo(t, true)
		gitIn(t, repo, "branch", "base", "origin/main")
		gitIn(t, repo, "update-ref", "-d", "refs/remotes/origin/main")
		made, err := c.New(NewOptions{Title: "Add x", Slug: "x", Worktrees: []string{repo}, Sessions: []string{"claude:s2"},
			Harness: "claude"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		setProp(t, made.Plan, "STATUS", "working")
		p, _ := c.Open(made.Plan)
		p.Record("", Event{Kind: "committed", Commit: "Add x", Session: "s2"}, time.Now())
		expectReason(t, "unreviewed", stopIn(t, c)("s2", repo, false), "no review receipt matches the change BASE..HEAD")
		p.Receipt("", Receipt{Range: "base..HEAD", PatchID: patchIDOf(t, repo, "base")}, time.Now())
		expectReason(t, "reviewed", stopIn(t, c)("s2", repo, false), "")
	})

	// A receipt counts only for a range from the plan's base for the
	// branch, or the default branch's remote-tracking ref, or below: one
	// for part of the change, however its patch ID matches, does not.
	t.Run("part of the change", func(t *testing.T) {
		for _, c := range []struct {
			name, branches, base, want string
		}{
			{"the last commit alone", "", "HEAD~1", "no review receipt matches the change origin/main..HEAD"},
			{"from the default ref", "", "origin/main", ""},
			{"from below the default ref", "", "origin/main~1", ""},
			{"from the plan's base", "parent..main", "parent", ""},
			{"above the plan's base", "parent..main", "HEAD~1", "no review receipt matches the change parent..HEAD"},
			{"the plan's base for another branch", "parent..other", "parent", "no review receipt matches the change origin/main..HEAD"},
		} {
			t.Run(c.name, func(t *testing.T) {
				cfg := testConfig(t)
				repo := stopRepo(t, false)
				commitFile(t, repo, "w", "Add w upstream")
				gitIn(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
				gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
				commitFile(t, repo, "x", "Add x")
				gitIn(t, repo, "branch", "parent")
				commitFile(t, repo, "y", "Add y")
				commitFile(t, repo, "z", "Add z")
				made, err := cfg.New(NewOptions{Title: "Add y", Slug: "y", Worktrees: []string{repo}, Sessions: []string{"claude:s2"},
					Branches: strings.Fields(c.branches), Harness: "claude"}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				setProp(t, made.Plan, "STATUS", "working")
				p, _ := cfg.Open(made.Plan)
				p.Record("", Event{Kind: "committed", Commit: "Add y", Session: "s2"}, time.Now())
				p.Receipt("", Receipt{Range: c.base + "..HEAD", PatchID: patchIDOf(t, repo, c.base)}, time.Now())
				expectReason(t, c.name, stopIn(t, cfg)("s2", repo, false), c.want)
			})
		}
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
		// step brings the plan, and the repository it is for, from the
		// stage before to this one.
		step func(t *testing.T, p Plan, repo string)
		// session and other are the session the hook runs in, and whether it
		// runs in another repository.
		session string
		other   bool
		want    string
	}
	const owed = "no review receipt matches the change origin/main..HEAD"
	receipt := func(t *testing.T, p Plan, repo string) {
		t.Helper()
		if _, err := p.Receipt("", Receipt{Range: "origin/main..HEAD", Tip: strings.TrimSpace(gitIn(t, repo, "rev-parse", "HEAD")),
			PatchID: patchID(t, repo), Reviewer: "codex"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	stages := []stage{
		{"planning", func(*testing.T, Plan, string) {}, "s2", false, ""},
		{"done, not verified", func(t *testing.T, p Plan, _ string) {
			setProp(t, p.File(), "STATUS", "working")
			p.Log(Event{Kind: "done", Item: "S1", Commit: "Add x"}, time.Now())
		}, "s2", false, "S1"},
		{"found by session", func(*testing.T, Plan, string) {}, "s2", true, "S1"},
		// The plan is s2's: another session in its worktree answers for
		// none of it.
		{"another session in its worktree", func(*testing.T, Plan, string) {}, "s1", false, ""},
		{"committed, with no receipt", func(t *testing.T, p Plan, _ string) {
			p.Log(Event{Kind: "verified", Item: "S1", Evidence: "tests pass"}, time.Now())
			p.Record("", Event{Kind: "committed", Commit: "Add x", Session: "s2"}, time.Now())
		}, "s2", false, owed},
		{"another session's commit in its worktree", func(t *testing.T, p Plan, _ string) {
			p.Record("", Event{Kind: "committed", Commit: "Add z", Session: "s1"}, time.Now())
		}, "s1", false, owed},
		// What an agent logs itself is no receipt.
		{"reviewed and handed back by the agent's word", func(t *testing.T, p Plan, _ string) {
			p.Log(Event{Kind: "reviewed", Item: p.Name, Summary: "round 1: 0 findings"}, time.Now())
			p.Log(Event{Kind: "handed-back", Item: p.Name}, time.Now())
		}, "s2", false, owed},
		{"reviewed", receipt, "s2", false, ""},
		{"reworded and rebased with the same change", func(t *testing.T, p Plan, repo string) {
			gitIn(t, repo, "commit", "-q", "--amend", "-m", "Add x, reworded")
			gitIn(t, repo, "switch", "-q", "-c", "upstream", "origin/main")
			commitFile(t, repo, "u", "Add u upstream")
			gitIn(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
			gitIn(t, repo, "switch", "-q", "main")
			gitIn(t, repo, "rebase", "-q", "origin/main")
		}, "s2", false, ""},
		{"changed since the receipt", func(t *testing.T, p Plan, repo string) {
			commitFile(t, repo, "y", "Add y")
			p.Record("", Event{Kind: "committed", Commit: "Add y", Session: "s2"}, time.Now())
		}, "s2", false, owed},
		{"reviewed again", receipt, "s2", false, ""},
		{"opted out", func(t *testing.T, p Plan, repo string) {
			commitFile(t, repo, "z", "Add z")
			if err := p.SetProperty("", "PIPELINE", []string{"opted-out:spike"}, time.Now()); err != nil {
				t.Fatal(err)
			}
		}, "s2", false, ""},
		{"halted", func(t *testing.T, p Plan, _ string) {
			if err := p.SetProperty("", "PIPELINE", []string{"full"}, time.Now()); err != nil {
				t.Fatal(err)
			}
			setProp(t, p.File(), "STATUS", "halted")
		}, "s2", false, ""},
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
				before.step(t, p, repo)
			}
			cwd := repo
			if s.other {
				cwd = other
			}
			expectReason(t, s.name, stopIn(t, c)(s.session, cwd, false), s.want)
		})
	}
}

// A review receipt is written by the review alone: the log keeps what it
// says, and an agent cannot log one.
func TestReceipt(t *testing.T) {
	p := newPlan(t, testConfig(t), "x")
	if _, err := p.Receipt("", Receipt{Range: "origin/main..HEAD", Tip: "1234", PatchID: "abcd", Reviewer: "subagent",
		Findings: 2}, day); err != nil {
		t.Fatal(err)
	}
	events, err := p.Events()
	if err != nil || len(events) == 0 {
		t.Fatalf("events %v, %v", events, err)
	}
	e := events[len(events)-1]
	if got := strings.Join([]string{e.Kind, e.Item, e.Range, e.Tip, e.PatchID, e.Agent, e.Summary}, "|"); got !=
		"review-receipt|"+p.Name+"|origin/main..HEAD|1234|abcd|subagent|origin/main..HEAD: 2 findings" {
		t.Errorf("receipt %s", got)
	}
	_, err = p.Log(Event{Kind: "review-receipt", Item: p.Name, Range: "origin/main..HEAD", PatchID: "abcd"}, day)
	isRefusal(t, "a receipt an agent logs", err)
}

// TestStopCheckChat covers ECA's postRequest hook, which has no
// stop_hook_active: a chat's stop is blocked once, logged as stop-blocked
// in its plan, and passes while its plan's log holds nothing newer than
// the stops it blocked.
func TestStopCheckChat(t *testing.T) {
	// stopIn returns a function that runs ECA's hook in repo for a chat.
	stopIn := func(t *testing.T, c Config, repo string) func(chat string) string {
		return func(chat string) string {
			t.Helper()
			event, _ := json.Marshal(map[string]any{"hook_type": "postRequest", "hook_name": "x", "chat_id": chat,
				"cwd": repo, "workspaces": []string{repo}, "follow_up_active": false})
			return c.StopCheckChat(event, time.Now())
		}
	}

	t.Run("nothing to sign", func(t *testing.T) {
		stop := stopIn(t, testConfig(t), stopRepo(t, false))
		if got := stop("c1"); got != "" {
			t.Fatalf("nothing to sign: %q", got)
		}
	})

	t.Run("one block per chat", func(t *testing.T) {
		const owed = "no review receipt matches"
		repo := stopRepo(t, true)
		c := testConfig(t)
		stop := stopIn(t, c, repo)
		commit := func(chat, subject string) Plan {
			t.Helper()
			p, _, err := c.RecordCommit(chat, repo, Event{Commit: subject, Summary: subject}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			return p
		}
		p := commit("c1", "Add x")
		commit("c2", "Add x")
		if got := stop("c1"); !strings.Contains(got, owed) {
			t.Fatalf("first stop: %q", got)
		}
		if got := stop("c2"); !strings.Contains(got, owed) {
			t.Errorf("another chat: %q", got)
		}
		if got := stop("c1"); got != "" {
			t.Errorf("second stop of the chat: %q", got)
		}
		if got := stop("c1"); got != "" {
			t.Errorf("a stop with nothing new: %q", got)
		}
		commit("c1", "Add y")
		if got := stop("c1"); !strings.Contains(got, owed) {
			t.Errorf("a stop after more work: %q", got)
		}
		events, _ := p.Events()
		var blocked []string
		for _, e := range events {
			if e.Kind == "stop-blocked" {
				blocked = append(blocked, e.Session)
			}
		}
		if strings.Join(blocked, " ") != "c1 c2 c1" {
			t.Errorf("stop-blocked for %v", blocked)
		}
	})

	t.Run("not json", func(t *testing.T) {
		c := testConfig(t)
		if got := c.StopCheckChat([]byte("x"), time.Now()); got != "" {
			t.Errorf("not json: %q", got)
		}
	})
}
