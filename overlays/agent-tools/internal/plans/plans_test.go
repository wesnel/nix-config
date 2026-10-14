package plans

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/refusal"
)

var day = time.Date(2026, 10, 14, 13, 5, 0, 0, time.Local)

func testConfig(t *testing.T) Config {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	return Config{Dir: filepath.Join(dir, "notes", "agents"), Host: "box"}
}

func newPlan(t *testing.T, c Config, slug string) Plan {
	t.Helper()
	made, err := c.New(NewOptions{Title: "Add the loop", Slug: slug, Repos: []string{"o/r"},
		Branches: []string{"main..loop"}, Worktrees: []string{"/w/loop"}, Sessions: []string{"claude:abc"},
		Harness: "claude"}, day)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// addItems puts headings with IDs under a plan file's heading.
func addItems(t *testing.T, file, under string, ids ...string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "*** TODO Step %s\n:PROPERTIES:\n:ID: %s\n:END:\n", id, id)
	}
	text := strings.Replace(string(data), under+"\n", under+"\n"+b.String(), 1)
	if text == string(data) {
		t.Fatalf("%s has no %q", file, under)
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setGoal(t *testing.T, file, goal string) {
	t.Helper()
	data, _ := os.ReadFile(file)
	text := strings.Replace(string(data), "** Goal\n", "** Goal\n"+goal+"\n", 1)
	os.WriteFile(file, []byte(text), 0o644)
}

// isRefusal checks err is a refusal that says what to do instead.
func isRefusal(t *testing.T, what string, err error) {
	t.Helper()
	var r refusal.Refusal
	switch {
	case !errors.As(err, &r):
		t.Errorf("%s: want a refusal, got %v", what, err)
	case !refusal.SaysInstead(r.Error()):
		t.Errorf("%s: %q does not say what to do instead", what, r.Error())
	}
}

// A plan is three kinds of file side by side in the plans directory, named
// for its date, slug and host.
func TestNew(t *testing.T) {
	t.Run("files", func(t *testing.T) {
		c := testConfig(t)
		made, err := c.New(NewOptions{Title: "Add the loop", Slug: "add-loop", Repos: []string{"o/r"},
			Branches: []string{"main..loop"}, Worktrees: []string{"/w/loop"}, Sessions: []string{"claude:abc"},
			Harness: "claude"}, day)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(c.Dir, "2026-10-14-add-loop.box.org")
		if made.Plan != file || made.File != file || made.ID != "2026-10-14-add-loop.box" ||
			made.Log != filepath.Join(c.Dir, "2026-10-14-add-loop.box.log.org") {
			t.Errorf("made %+v", made)
		}
		data, _ := os.ReadFile(made.File)
		for _, s := range []string{"#+title: Add the loop\n", "#+TODO: TODO NEXT | DONE\n", "* Plan\n:PROPERTIES:\n",
			":ID: 2026-10-14-add-loop.box\n", ":HOST: box\n", ":REPOSITORIES: o/r\n", ":BRANCHES: main..loop\n",
			":WORKTREES: /w/loop\n", ":SESSIONS: claude:abc\n", ":HARNESS: claude\n", ":STATUS: planning\n",
			":PIPELINE: full\n", "** Goal\n", "** Decisions\n", "** Steps\n", "** Distribution\n",
			"** Open questions\n", "** Verification\n"} {
			if !strings.Contains(string(data), s) {
				t.Errorf("the plan lacks %q:\n%s", s, data)
			}
		}
		if _, err := os.Stat(made.Log); err != nil {
			t.Errorf("no log: %v", err)
		}
		entries, _ := os.ReadDir(c.Dir)
		for _, e := range entries {
			if e.IsDir() {
				t.Errorf("New made a directory: %s", e.Name())
			}
		}
	})

	t.Run("second plan with the slug", func(t *testing.T) {
		c := testConfig(t)
		newPlan(t, c, "add-loop")
		again, err := c.New(NewOptions{Title: "Again", Slug: "add-loop"}, day)
		if err != nil || again.File != filepath.Join(c.Dir, "2026-10-14-add-loop-2.box.org") {
			t.Errorf("second plan with the slug: %+v, %v", again, err)
		}
	})

	for what, o := range map[string]NewOptions{
		"a slug with spaces":    {Title: "T", Slug: "Add loop"},
		"no title":              {Slug: "x"},
		"a title with newlines": {Title: "T\n* evil", Slug: "x"},
		"a worktree with space": {Title: "T", Slug: "x", Worktrees: []string{"/a b"}},
		"a part without parent": {Title: "T", Part: "P1"},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			c := testConfig(t)
			_, err := c.New(o, day)
			isRefusal(t, what, err)
		})
	}
}

func TestOpen(t *testing.T) {
	// withPart makes a plan with a part, P1.
	withPart := func(t *testing.T) (Config, Plan) {
		t.Helper()
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Distribution", "P1")
		if _, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day); err != nil {
			t.Fatal(err)
		}
		return c, p
	}

	for name, arg := range map[string]func(Plan) string{
		"plan file": func(p Plan) string { return p.file("") },
		"part file": func(p Plan) string { return p.file("P1") },
		"log":       func(p Plan) string { return p.log() },
		"name":      func(Plan) string { return "2026-10-14-x.box" },
	} {
		t.Run("by "+name, func(t *testing.T) {
			c, p := withPart(t)
			arg := arg(p)
			if got, err := c.Open(arg); err != nil || got.Name != "2026-10-14-x.box" || got.file("") != p.file("") {
				t.Errorf("Open(%s) = %+v, %v", arg, got, err)
			}
		})
	}

	// Each case names a plan to open, given the plans directory and a
	// directory outside it that holds a plan and its log.
	for name, arg := range map[string]func(dir, outside string) string{
		"plan outside the directory":    func(_, outside string) string { return filepath.Join(outside, "2026-10-14-x.box.org") },
		"the directory":                 func(dir, _ string) string { return dir },
		"another file in the directory": func(dir, _ string) string { return filepath.Join(dir, "notes.org") },
		"path out of the directory":     func(dir, _ string) string { return dir + "/../../elsewhere" },
		"unknown name":                  func(string, string) string { return "2026-10-14-y.box" },
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			c, _ := withPart(t)
			outside := t.TempDir()
			os.WriteFile(filepath.Join(outside, "2026-10-14-x.box.org"), []byte("* Plan\n"), 0o644)
			os.WriteFile(filepath.Join(outside, "2026-10-14-x.box.log.org"), nil, 0o644)
			os.WriteFile(filepath.Join(c.Dir, "notes.org"), nil, 0o644)
			arg := arg(c.Dir, outside)
			_, err := c.Open(arg)
			isRefusal(t, "Open "+arg, err)
		})
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, gitenv.Without(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestInit(t *testing.T) {
	t.Run("in a repository", testInitInRepository)
	t.Run("clone without the key", testInitCloneWithoutKey)
	t.Run("clone with the key", testInitCloneWithKey)
}

// Init readies the plans directory for a session whose sandbox may write
// only there, keeping the logs' lock files out of the repository around
// it, and changes nothing else.
func testInitInRepository(t *testing.T) {
	c := testConfig(t)
	outer := filepath.Dir(filepath.Dir(c.Dir))
	gitIn(t, outer, "init", "-q", "-b", "main")
	before, _ := os.ReadFile(filepath.Join(outer, ".git", "info", "exclude"))
	for i := 0; i < 2; i++ {
		got, err := c.Init()
		if err != nil || got.Dir != c.Dir {
			t.Fatalf("Init: %+v, %v", got, err)
		}
	}
	if ignore, _ := os.ReadFile(filepath.Join(c.Dir, ".gitignore")); string(ignore) != "*.lock\n" {
		t.Errorf(".gitignore: %q", ignore)
	}
	if after, _ := os.ReadFile(filepath.Join(outer, ".git", "info", "exclude")); string(before) != string(after) {
		t.Errorf("the repository's exclude changed:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(c.Dir), ".git")); err == nil {
		t.Error("Init made a repository")
	}
	newPlan(t, c, "x")
	status := gitIn(t, outer, "status", "--porcelain", "--untracked-files=all")
	if strings.Contains(status, ".lock") {
		t.Errorf("a lock file shows in the repository:\n%s", status)
	}
}

func TestPart(t *testing.T) {
	// distributing makes a plan that distributes P1.
	distributing := func(t *testing.T) (Config, Plan) {
		t.Helper()
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Distribution", "P1")
		return c, p
	}

	t.Run("made", func(t *testing.T) {
		c, p := distributing(t)
		made, err := c.New(NewOptions{Title: "The loop's part", Parent: p.file(""), Part: "P1",
			Branches: []string{"loop..part"}}, day)
		if err != nil {
			t.Fatal(err)
		}
		if made.File != filepath.Join(c.Dir, "2026-10-14-x.box.P1.org") || made.Plan != p.file("") || made.ID != "P1" ||
			made.Log != p.log() {
			t.Errorf("made %+v", made)
		}
		data, _ := os.ReadFile(made.File)
		for _, s := range []string{"#+title: The loop's part\n", ":ID: P1\n", ":PARENT: [[file:2026-10-14-x.box.org]]\n",
			":BRANCHES: loop..part\n", "** Goal\n", "** Steps\n"} {
			if !strings.Contains(string(data), s) {
				t.Errorf("the part lacks %q:\n%s", s, data)
			}
		}
	})

	t.Run("refused/not distributed", func(t *testing.T) {
		c, p := distributing(t)
		_, err := c.New(NewOptions{Title: "T", Parent: p.file(""), Part: "P2"}, day)
		isRefusal(t, "a part the parent does not distribute", err)
	})

	t.Run("refused/made twice", func(t *testing.T) {
		c, p := distributing(t)
		if _, err := c.New(NewOptions{Title: "The loop's part", Parent: p.file(""), Part: "P1"}, day); err != nil {
			t.Fatal(err)
		}
		_, err := c.New(NewOptions{Title: "T", Parent: p.file(""), Part: "P1"}, day)
		isRefusal(t, "a part made twice", err)
	})

	t.Run("refused/no ID", func(t *testing.T) {
		c, p := distributing(t)
		_, err := c.New(NewOptions{Title: "T", Parent: p.file(""), Part: "../x"}, day)
		isRefusal(t, "a part that is no ID", err)
	})
}

// Another host's plan sits beside this host's and can be read, but only its
// own host writes it.
func TestOtherHost(t *testing.T) {
	// theirs makes a plan of another host, with a step S1 and a part P1, and
	// opens it as this host.
	theirs := func(t *testing.T) (Config, Plan) {
		t.Helper()
		c := testConfig(t)
		theirs := newPlan(t, Config{Dir: c.Dir, Host: "other"}, "x")
		addItems(t, theirs.file(""), "** Steps", "S1")
		addItems(t, theirs.file(""), "** Distribution", "P1")
		p, err := c.Open(theirs.file(""))
		if err != nil || p.Name != "2026-10-14-x.other" {
			t.Fatalf("Open: %+v, %v", p, err)
		}
		return c, p
	}

	t.Run("read", func(t *testing.T) {
		_, p := theirs(t)
		if _, err := p.Status(); err != nil {
			t.Errorf("status: %v", err)
		}
	})

	t.Run("refused/log", func(t *testing.T) {
		_, p := theirs(t)
		before, _ := os.ReadFile(p.log())
		_, err := p.Log(Event{Kind: "started", Item: "S1"}, day)
		isRefusal(t, "logging to another host's plan", err)
		if after, _ := os.ReadFile(p.log()); string(after) != string(before) {
			t.Errorf("another host's log changed:\n%s", after)
		}
	})

	t.Run("refused/part", func(t *testing.T) {
		c, p := theirs(t)
		before, _ := os.ReadFile(p.log())
		_, err := c.New(NewOptions{Title: "T", Parent: p.file(""), Part: "P1"}, day)
		isRefusal(t, "a part of another host's plan", err)
		if after, _ := os.ReadFile(p.log()); string(after) != string(before) {
			t.Errorf("another host's log changed:\n%s", after)
		}
	})
}

func TestLog(t *testing.T) {
	// withSteps makes a plan with steps S1 and S2.
	withSteps := func(t *testing.T) Plan {
		t.Helper()
		p := newPlan(t, testConfig(t), "x")
		addItems(t, p.file(""), "** Steps", "S1", "S2")
		return p
	}

	t.Run("appends events", func(t *testing.T) {
		p := withSteps(t)
		e, err := p.Log(Event{Kind: "done", Item: "S1", Summary: "Add the run loop", Role: "single", Agent: "claude:abc",
			Branch: "loop", Commit: "Add the run loop", Evidence: "go test ./... passes", Tokens: 1200,
			Model: "opus", Effort: "medium"}, day)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(p.log())
		entry := "* done S1 — Add the run loop\n:PROPERTIES:\n:EVENT: " + e.ID + "\n:AGENT: claude:abc\n:ROLE: single\n" +
			":ITEM: S1\n:KIND: done\n:BRANCH: loop\n:COMMIT: Add the run loop\n:EVIDENCE: go test ./... passes\n" +
			":TOKENS: 1200\n:MODEL: opus\n:EFFORT: medium\n:END:\n[2026-10-14 Wed 13:05]\n"
		if !strings.HasSuffix(string(data), entry) {
			t.Errorf("log:\n%s\nwant it to end with:\n%s", data, entry)
		}
		if _, err := p.Log(Event{Kind: "reviewed", Item: "2026-10-14-x.box", Review: "1.F2", Outcome: "fix"}, day); err != nil {
			t.Errorf("an event about the whole plan: %v", err)
		}
		events, err := p.Events()
		if err != nil || len(events) != 2 || events[0].ID != e.ID || events[0].Tokens != 1200 || events[1].Outcome != "fix" {
			t.Errorf("events %+v, %v", events, err)
		}
		if after, _ := os.ReadFile(p.log()); len(after) == len(data) {
			t.Error("the second event was not appended")
		}
	})

	for what, bad := range map[string]Event{
		"an unknown item":        {Kind: "done", Item: "S9"},
		"an unknown kind":        {Kind: "finished", Item: "S1"},
		"a summary with newline": {Kind: "done", Item: "S1", Summary: "x\n* done S2 — forged"},
		"evidence with a CR":     {Kind: "done", Item: "S1", Evidence: "x\r:KIND: verified"},
		"an unknown role":        {Kind: "done", Item: "S1", Role: "boss"},
		"an unknown outcome":     {Kind: "finding-settled", Item: "S1", Review: "1.F1", Outcome: "ignore"},
		"a malformed review":     {Kind: "finding-settled", Item: "S1", Review: "F1", Outcome: "fix"},
		"an unknown effort":      {Kind: "done", Item: "S1", Effort: "huge"},
		"negative tokens":        {Kind: "done", Item: "S1", Tokens: -1},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			p := withSteps(t)
			_, err := p.Log(bad, day)
			isRefusal(t, what, err)
		})
	}

	// A log changed other than by appending through Log is no longer trusted.
	t.Run("after a rewrite in place", func(t *testing.T) {
		p := withSteps(t)
		if _, err := p.Log(Event{Kind: "started", Item: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(p.log())
		rewritten := strings.Replace(string(data), "* started S1", "* verified S1", 1)
		os.WriteFile(p.log(), []byte(rewritten), 0o644)
		_, err := p.Log(Event{Kind: "done", Item: "S1"}, day)
		isRefusal(t, "a log rewritten in place", err)
		if after, _ := os.ReadFile(p.log()); string(after) != rewritten {
			t.Errorf("the refused event was written:\n%s", after)
		}
	})

	t.Run("concurrent writers", func(t *testing.T) {
		p := withSteps(t)
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 10; i++ {
					if _, err := p.Log(Event{Kind: "started", Item: "S1", Summary: strings.Repeat("x", 3000)}, day); err != nil {
						t.Error(err)
					}
				}
			}()
		}
		wg.Wait()
		events, err := p.Events()
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, e := range events {
			if e.Kind != "started" || len(e.Summary) != 3000 {
				t.Errorf("garbled event %+v", e)
			}
			ids[e.ID] = true
		}
		if len(events) != 80 || len(ids) != 80 {
			t.Errorf("%d events, %d distinct", len(events), len(ids))
		}
		if _, err := p.Log(Event{Kind: "done", Item: "S1"}, day); err != nil {
			t.Errorf("after concurrent writers: %v", err)
		}
	})
}

func TestStatus(t *testing.T) {
	t.Run("items and tokens", testStatusItems)
	t.Run("ready parts", testStatusReadyParts)
}

func testStatusItems(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	plan := p.file("")
	addItems(t, plan, "** Steps", "S1", "S2", "S3")
	addItems(t, plan, "** Distribution", "P1")
	part, err := c.New(NewOptions{Title: "Part", Parent: plan, Part: "P1"}, day)
	if err != nil {
		t.Fatal(err)
	}
	addItems(t, part.File, "** Steps", "P1.S1")
	for _, e := range []Event{
		{Kind: "done", Item: "S1", Commit: "Add the run loop", Tokens: 100, Model: "opus", Effort: "low"},
		{Kind: "verified", Item: "S1", Tokens: 50, Model: "opus", Effort: "low"},
		{Kind: "done", Item: "S2", Commit: "Add the flag", Tokens: 30, Model: "sonnet", Effort: "medium"},
		{Kind: "done", Item: "P1.S1", Commit: "Add the part", Tokens: 7, Model: "sonnet", Effort: "medium"},
		{Kind: "reviewed", Item: "P1", Tokens: 3, Model: "opus", Effort: "high"},
	} {
		if _, err := p.Log(e, day); err != nil {
			t.Fatal(err)
		}
	}
	s, err := p.Status()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.NotDone, " ") != "S3" || strings.Join(s.DoneNotVerified, " ") != "S2 P1.S1" ||
		s.Implemented != "1/4" || s.Verified != 1 || s.Total != 4 || s.State != "planning" || s.Title != "Add the loop" ||
		s.Plan != plan {
		t.Errorf("status %+v", s)
	}
	if s.Tokens != 190 || s.TokensByPart[""] != 180 || s.TokensByPart["P1"] != 10 {
		t.Errorf("tokens %d by part %v", s.Tokens, s.TokensByPart)
	}
	found := false
	for _, r := range s.TokensBy {
		found = found || (r == TokenRow{Kind: "done", Model: "sonnet", Effort: "medium", Events: 2, Tokens: 37})
	}
	if !found {
		t.Errorf("tokens by kind and model %+v", s.TokensBy)
	}
	org := s.Org()
	for _, line := range []string{"| S1 | Step S1 | yes | yes | Add the run loop |", "| P1.S1 | Step P1.S1 | yes | no | Add the part |",
		"| S3 | Step S3 | no | no | |", "- Implemented: 1/4 verified", "- Without done: S3", "- Done, not verified: S2, P1.S1",
		"| P1 | 10 |", "| done | sonnet | medium | 2 | 37 |"} {
		if !strings.Contains(org, line) {
			t.Errorf("org status lacks %q:\n%s", line, org)
		}
	}
	newPlan(t, Config{Dir: c.Dir, Host: "other"}, "y")
	all, err := c.Tokens()
	if err != nil || len(all) == 0 {
		t.Errorf("tokens over every plan: %v, %v", all, err)
	}
}

func TestGoal(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		_, err := p.Goal("")
		isRefusal(t, "an empty goal", err)
	})

	t.Run("set", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		setGoal(t, p.file(""), "Add the loop.\n\nNot this change's job: the flag.")
		if goal, err := p.Goal(""); err != nil || goal != "Add the loop.\n\nNot this change's job: the flag.\n" {
			t.Errorf("goal %q, %v", goal, err)
		}
	})

	t.Run("part", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Distribution", "P1")
		part, _ := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day)
		setGoal(t, part.File, "The part.")
		if goal, err := p.Goal("P1"); err != nil || goal != "The part.\n" {
			t.Errorf("part goal %q, %v", goal, err)
		}
	})

	t.Run("part with no file", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		_, err := p.Goal("P7")
		isRefusal(t, "a part with no file", err)
	})
}

func TestFind(t *testing.T) {
	// twoHosts makes a plan on this host and one of the same name on
	// another, and returns this host's.
	twoHosts := func(t *testing.T) (Config, Plan) {
		t.Helper()
		c := testConfig(t)
		p := newPlan(t, c, "x")
		newPlan(t, Config{Dir: c.Dir, Host: "other"}, "x")
		return c, p
	}

	for what, o := range map[string]FindOptions{
		"session": {Session: "abc"}, "harness session": {Session: "claude:abc"},
		"worktree": {Worktree: "/w/loop"}, "branch": {Branch: "loop"},
	} {
		t.Run("by "+what, func(t *testing.T) {
			c, p := twoHosts(t)
			found, err := c.Find(o)
			if err != nil || len(found) != 1 || found[0].Plan != p.file("") || found[0].File != p.file("") {
				t.Errorf("by %s: %+v, %v", what, found, err)
			}
		})
	}

	for what, o := range map[string]FindOptions{
		"part of a session":  {Session: "ab"},
		"parent of worktree": {Worktree: "/w"},
		"another branch":     {Branch: "main"},
	} {
		t.Run("none/"+what, func(t *testing.T) {
			c, _ := twoHosts(t)
			if found, err := c.Find(o); err != nil || len(found) != 0 {
				t.Errorf("%+v found %+v, %v", o, found, err)
			}
		})
	}
}

func hookContext(t *testing.T, out []byte, event string) string {
	t.Helper()
	var v struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &v); err != nil || v.HookSpecificOutput.HookEventName != event {
		t.Fatalf("hook output %s: %v", out, err)
	}
	return v.HookSpecificOutput.AdditionalContext
}

func TestImportClaude(t *testing.T) {
	// imported records an approved plan from session s1, and returns the
	// plan's file.
	imported := func(t *testing.T, c Config) string {
		t.Helper()
		markdown := "# Add the loop\n\n* a bullet\n#+begin_src sh\n,* escaped already\n"
		hook, _ := json.Marshal(map[string]any{"session_id": "s1", "cwd": t.TempDir(), "hook_event_name": "PostToolUse",
			"tool_name": "ExitPlanMode", "tool_input": map[string]string{"plan": "stale"},
			"tool_response": map[string]string{"plan": markdown, "filePath": "/h/.claude/plans/x.md"}})
		out, err := c.ImportClaude(hook, day)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(c.Dir, "2026-10-14-add-the-loop.box.org")
		if context := hookContext(t, out, "PostToolUse"); !strings.Contains(context, file) || !strings.Contains(context, "Goal") {
			t.Errorf("context %q", context)
		}
		return file
	}

	t.Run("approved plan", func(t *testing.T) {
		file := imported(t, testConfig(t))
		data, _ := os.ReadFile(file)
		want := "* Approved plan\n:PROPERTIES:\n:SOURCE: /h/.claude/plans/x.md\n:END:\n#+begin_src markdown\n" +
			"# Add the loop\n\n,* a bullet\n,#+begin_src sh\n,,* escaped already\n#+end_src\n"
		if !strings.Contains(string(data), want) || !strings.Contains(string(data), ":SESSIONS: claude:s1\n") {
			t.Errorf("the plan:\n%s\nwant:\n%s", data, want)
		}
	})

	// The same session's next plan joins the first, from tool_input when the
	// response has none.
	t.Run("the session's next plan", func(t *testing.T) {
		c := testConfig(t)
		file := imported(t, c)
		hook, _ := json.Marshal(map[string]any{"session_id": "s1", "tool_name": "ExitPlanMode",
			"tool_input": map[string]string{"plan": "Second"}})
		if _, err := c.ImportClaude(hook, day); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(file)
		if strings.Count(string(data), "* Approved plan\n") != 2 || !strings.Contains(string(data), "Second\n#+end_src") {
			t.Errorf("the plan after a second plan:\n%s", data)
		}
	})

	t.Run("no plan", func(t *testing.T) {
		c := testConfig(t)
		if out, err := c.ImportClaude([]byte(`{"session_id":"s2","tool_name":"ExitPlanMode"}`), day); err != nil || len(out) != 0 {
			t.Errorf("no plan: %s, %v", out, err)
		}
	})
}

func TestSessionHook(t *testing.T) {
	t.Run("the session's plan", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		out, err := c.SessionHook([]byte(`{"session_id":"abc","source":"compact","cwd":"/nowhere"}`))
		if err != nil {
			t.Fatal(err)
		}
		if context := hookContext(t, out, "SessionStart"); !strings.Contains(context, p.file("")) {
			t.Errorf("context %q", context)
		}
	})

	t.Run("no plan", func(t *testing.T) {
		c := testConfig(t)
		newPlan(t, c, "x")
		if out, err := c.SessionHook([]byte(`{"session_id":"zzz","source":"resume","cwd":"/nowhere"}`)); err != nil || len(out) != 0 {
			t.Errorf("no plan: %s, %v", out, err)
		}
	})
}

func TestLoad(t *testing.T) {
	// useConfig points AGENT_TOOLS_CONFIG at a file of the test's own, with
	// the content given, or none.
	useConfig := func(t *testing.T, content string) {
		t.Helper()
		config := filepath.Join(t.TempDir(), "config.json")
		t.Setenv("AGENT_TOOLS_CONFIG", config)
		if content != "" {
			os.WriteFile(config, []byte(content), 0o644)
		}
	}

	t.Run("without a config", func(t *testing.T) {
		useConfig(t, "")
		home, _ := os.UserHomeDir()
		if c, err := Load(); err != nil || c.Dir != filepath.Join(home, "git", "notes", "agents") || c.Host == "" {
			t.Errorf("without a config: %+v, %v", c, err)
		}
	})

	t.Run("from the config", func(t *testing.T) {
		useConfig(t, `{"repos": {}, "plans": {"dir": "/p", "host": "ngrok"}}`)
		if c, err := Load(); err != nil || c.Dir != "/p" || c.Host != "ngrok" {
			t.Errorf("from the config: %+v, %v", c, err)
		}
	})

	// A dot would make a plan's file names ambiguous.
	t.Run("host with a dot", func(t *testing.T) {
		useConfig(t, `{"plans": {"dir": "/p", "host": "a.b"}}`)
		if c, err := Load(); err == nil {
			t.Errorf("a host with a dot: %+v", c)
		}
	})
}
