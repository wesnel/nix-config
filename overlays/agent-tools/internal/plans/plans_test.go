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
	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
	"github.com/wesnel/agent-tools/internal/refusal"
)

var day = time.Date(2026, 10, 14, 13, 5, 0, 0, time.Local)

func testConfig(t *testing.T) Config {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	return Config{Dir: filepath.Join(dir, "org", "agents"), Host: "box"}
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

func TestNew(t *testing.T) {
	c := testConfig(t)
	made, err := c.New(NewOptions{Title: "Add the loop", Slug: "add-loop", Repos: []string{"o/r"},
		Branches: []string{"main..loop"}, Worktrees: []string{"/w/loop"}, Sessions: []string{"claude:abc"},
		Harness: "claude"}, day)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(c.Dir, "box", "2026-10-14-add-loop")
	if made.Plan != want || made.File != filepath.Join(want, "plan.org") || made.ID != "2026-10-14-add-loop" {
		t.Errorf("made %+v", made)
	}
	data, _ := os.ReadFile(made.File)
	for _, s := range []string{"#+title: Add the loop\n", "#+TODO: TODO NEXT | DONE\n", "* Plan\n:PROPERTIES:\n",
		":ID: 2026-10-14-add-loop\n", ":HOST: box\n", ":REPOSITORIES: o/r\n", ":BRANCHES: main..loop\n",
		":WORKTREES: /w/loop\n", ":SESSIONS: claude:abc\n", ":HARNESS: claude\n", ":STATUS: planning\n",
		":PIPELINE: full\n", "** Goal\n", "** Decisions\n", "** Steps\n", "** Distribution\n",
		"** Open questions\n", "** Verification\n"} {
		if !strings.Contains(string(data), s) {
			t.Errorf("plan.org lacks %q:\n%s", s, data)
		}
	}
	if _, err := os.Stat(made.Log); err != nil {
		t.Errorf("no log: %v", err)
	}
	again, err := c.New(NewOptions{Title: "Again", Slug: "add-loop"}, day)
	if err != nil || filepath.Base(again.Plan) != "2026-10-14-add-loop-2" {
		t.Errorf("second plan with the slug: %+v, %v", again, err)
	}
	for what, o := range map[string]NewOptions{
		"a slug with spaces":    {Title: "T", Slug: "Add loop"},
		"no title":              {Slug: "x"},
		"a title with newlines": {Title: "T\n* evil", Slug: "x"},
		"a worktree with space": {Title: "T", Slug: "x", Worktrees: []string{"/a b"}},
		"a part without parent": {Title: "T", Part: "P1"},
	} {
		_, err := c.New(o, day)
		isRefusal(t, what, err)
	}
}

func TestOpen(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	for _, arg := range []string{p.Dir, filepath.Join(p.Dir, "plan.org"), filepath.Join(p.Dir, "log.org")} {
		if got, err := c.Open(arg); err != nil || got.Dir != p.Dir || got.Name != "2026-10-14-x" {
			t.Errorf("Open(%s) = %+v, %v", arg, got, err)
		}
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "plan.org"), []byte("* Plan\n"), 0o644)
	os.WriteFile(filepath.Join(outside, "log.org"), nil, 0o644)
	for _, arg := range []string{outside, filepath.Join(c.Dir, "box"), c.Dir + "/box/../../elsewhere"} {
		_, err := c.Open(arg)
		isRefusal(t, "Open "+arg, err)
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

// The plans directory stays out of the repository it sits in, the one the
// ring commits to, through that repository's own exclude file, whatever
// repository the caller's environment names.
func TestExclude(t *testing.T) {
	c := testConfig(t)
	org := filepath.Dir(c.Dir)
	os.MkdirAll(org, 0o755)
	gitIn(t, org, "init", "-q", "-b", "main")
	gitenvtest.Decoy(t)
	newPlan(t, c, "x")
	newPlan(t, c, "y")
	exclude, _ := os.ReadFile(filepath.Join(org, ".git", "info", "exclude"))
	if strings.Count(string(exclude), "/agents/\n") != 1 {
		t.Errorf("exclude:\n%s", exclude)
	}
	os.Unsetenv("GIT_DIR")
	os.Unsetenv("GIT_WORK_TREE")
	if status := gitIn(t, org, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Errorf("the plans show in the repository:\n%s", status)
	}
}

func TestExcludeOnlyInItsOwnRepository(t *testing.T) {
	c := testConfig(t)
	// ~/git/org is no repository of its own, though a directory above it is.
	outer := filepath.Dir(filepath.Dir(c.Dir))
	gitIn(t, outer, "init", "-q", "-b", "main")
	before, _ := os.ReadFile(filepath.Join(outer, ".git", "info", "exclude"))
	made, err := c.New(NewOptions{Title: "T", Slug: "x"}, day)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(outer, ".git", "info", "exclude"))
	if string(before) != string(after) || made.Excluded != "" {
		t.Errorf("the outer repository's exclude changed:\n%s\n%+v", after, made)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(c.Dir), ".git")); err == nil {
		t.Error("New made a repository")
	}
}

// Init readies this host's directory, and the exclude, for a session whose
// sandbox may write only there.
func TestInit(t *testing.T) {
	c := testConfig(t)
	org := filepath.Dir(c.Dir)
	os.MkdirAll(org, 0o755)
	gitIn(t, org, "init", "-q", "-b", "main")
	for i := 0; i < 2; i++ {
		got, err := c.Init()
		if err != nil || got.Dir != filepath.Join(c.Dir, "box") || got.Excluded != filepath.Join(org, ".git", "info", "exclude") {
			t.Fatalf("Init: %+v, %v", got, err)
		}
	}
	if info, err := os.Stat(filepath.Join(c.Dir, "box")); err != nil || !info.IsDir() {
		t.Errorf("no host directory: %v", err)
	}
	if exclude, _ := os.ReadFile(filepath.Join(org, ".git", "info", "exclude")); strings.Count(string(exclude), "/agents/\n") != 1 {
		t.Errorf("exclude:\n%s", exclude)
	}
	elsewhere := testConfig(t)
	if got, err := elsewhere.Init(); err != nil || got.Excluded != "" {
		t.Errorf("Init outside a repository: %+v, %v", got, err)
	}
}

func TestPart(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	addItems(t, filepath.Join(p.Dir, "plan.org"), "** Distribution", "P1")
	made, err := c.New(NewOptions{Title: "The loop's part", Parent: p.Dir, Part: "P1",
		Branches: []string{"loop..part"}}, day)
	if err != nil {
		t.Fatal(err)
	}
	if made.File != filepath.Join(p.Dir, "P1.org") || made.ID != "P1" || made.Log != filepath.Join(p.Dir, "log.org") {
		t.Errorf("made %+v", made)
	}
	data, _ := os.ReadFile(made.File)
	for _, s := range []string{"#+title: The loop's part\n", ":ID: P1\n", ":PARENT: [[file:plan.org]]\n",
		":BRANCHES: loop..part\n", "** Goal\n", "** Steps\n"} {
		if !strings.Contains(string(data), s) {
			t.Errorf("P1.org lacks %q:\n%s", s, data)
		}
	}
	_, err = c.New(NewOptions{Title: "T", Parent: p.Dir, Part: "P2"}, day)
	isRefusal(t, "a part the parent does not distribute", err)
	_, err = c.New(NewOptions{Title: "T", Parent: p.Dir, Part: "P1"}, day)
	isRefusal(t, "a part made twice", err)
	_, err = c.New(NewOptions{Title: "T", Parent: p.Dir, Part: "../x"}, day)
	isRefusal(t, "a part that is no ID", err)
}

func TestLog(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	addItems(t, filepath.Join(p.Dir, "plan.org"), "** Steps", "S1", "S2")
	e, err := p.Log(Event{Kind: "done", Item: "S1", Summary: "Add the run loop", Role: "single", Agent: "claude:abc",
		Branch: "loop", Commit: "Add the run loop", Evidence: "go test ./... passes", Tokens: 1200,
		Model: "opus", Effort: "medium"}, day)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(p.Dir, "log.org"))
	entry := "* done S1 — Add the run loop\n:PROPERTIES:\n:EVENT: " + e.ID + "\n:AGENT: claude:abc\n:ROLE: single\n" +
		":ITEM: S1\n:KIND: done\n:BRANCH: loop\n:COMMIT: Add the run loop\n:EVIDENCE: go test ./... passes\n" +
		":TOKENS: 1200\n:MODEL: opus\n:EFFORT: medium\n:END:\n[2026-10-14 Wed 13:05]\n"
	if !strings.HasSuffix(string(data), entry) {
		t.Errorf("log.org:\n%s\nwant it to end with:\n%s", data, entry)
	}
	if _, err := p.Log(Event{Kind: "reviewed", Item: "2026-10-14-x", Review: "1.F2", Outcome: "fix"}, day); err != nil {
		t.Errorf("an event about the whole plan: %v", err)
	}
	events, err := p.Events()
	if err != nil || len(events) != 2 || events[0].ID != e.ID || events[0].Tokens != 1200 || events[1].Outcome != "fix" {
		t.Errorf("events %+v, %v", events, err)
	}
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
		_, err := p.Log(bad, day)
		isRefusal(t, what, err)
	}
	if after, _ := os.ReadFile(filepath.Join(p.Dir, "log.org")); len(after) == len(data) {
		t.Error("the second event was not appended")
	}
}

// A log changed other than by appending through Log is no longer trusted.
func TestLogAfterRewrite(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	addItems(t, filepath.Join(p.Dir, "plan.org"), "** Steps", "S1")
	if _, err := p.Log(Event{Kind: "started", Item: "S1"}, day); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(p.Dir, "log.org")
	data, _ := os.ReadFile(log)
	rewritten := strings.Replace(string(data), "* started S1", "* verified S1", 1)
	os.WriteFile(log, []byte(rewritten), 0o644)
	_, err := p.Log(Event{Kind: "done", Item: "S1"}, day)
	isRefusal(t, "a log rewritten in place", err)
	if after, _ := os.ReadFile(log); string(after) != rewritten {
		t.Errorf("the refused event was written:\n%s", after)
	}
}

func TestConcurrentLog(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	addItems(t, filepath.Join(p.Dir, "plan.org"), "** Steps", "S1")
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
}

func TestStatus(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	plan := filepath.Join(p.Dir, "plan.org")
	addItems(t, plan, "** Steps", "S1", "S2", "S3")
	addItems(t, plan, "** Distribution", "P1")
	part, err := c.New(NewOptions{Title: "Part", Parent: p.Dir, Part: "P1"}, day)
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
		s.Implemented != "1/4" || s.Verified != 1 || s.Total != 4 || s.State != "planning" || s.Title != "Add the loop" {
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
	all, err := c.Tokens()
	if err != nil || len(all) == 0 {
		t.Errorf("tokens over every plan: %v, %v", all, err)
	}
}

func TestGoal(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	_, err := p.Goal("")
	isRefusal(t, "an empty goal", err)
	setGoal(t, filepath.Join(p.Dir, "plan.org"), "Add the loop.\n\nNot this change's job: the flag.")
	if goal, err := p.Goal(""); err != nil || goal != "Add the loop.\n\nNot this change's job: the flag.\n" {
		t.Errorf("goal %q, %v", goal, err)
	}
	addItems(t, filepath.Join(p.Dir, "plan.org"), "** Distribution", "P1")
	part, _ := c.New(NewOptions{Title: "Part", Parent: p.Dir, Part: "P1"}, day)
	setGoal(t, part.File, "The part.")
	if goal, err := p.Goal("P1"); err != nil || goal != "The part.\n" {
		t.Errorf("part goal %q, %v", goal, err)
	}
	_, err = p.Goal("P7")
	isRefusal(t, "a part with no file", err)
}

func TestFind(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	newPlan(t, Config{Dir: c.Dir, Host: "other"}, "x")
	for what, o := range map[string]FindOptions{
		"session": {Session: "abc"}, "harness session": {Session: "claude:abc"},
		"worktree": {Worktree: "/w/loop"}, "branch": {Branch: "loop"},
	} {
		found, err := c.Find(o)
		if err != nil || len(found) != 1 || found[0].Plan != p.Dir || found[0].File != filepath.Join(p.Dir, "plan.org") {
			t.Errorf("by %s: %+v, %v", what, found, err)
		}
	}
	for _, o := range []FindOptions{{Session: "ab"}, {Worktree: "/w"}, {Branch: "main"}} {
		if found, err := c.Find(o); err != nil || len(found) != 0 {
			t.Errorf("%+v found %+v, %v", o, found, err)
		}
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
	c := testConfig(t)
	markdown := "# Add the loop\n\n* a bullet\n#+begin_src sh\n,* escaped already\n"
	hook, _ := json.Marshal(map[string]any{"session_id": "s1", "cwd": t.TempDir(), "hook_event_name": "PostToolUse",
		"tool_name": "ExitPlanMode", "tool_input": map[string]string{"plan": "stale"},
		"tool_response": map[string]string{"plan": markdown, "filePath": "/h/.claude/plans/x.md"}})
	out, err := c.ImportClaude(hook, day)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(c.Dir, "box", "2026-10-14-add-the-loop", "plan.org")
	if context := hookContext(t, out, "PostToolUse"); !strings.Contains(context, file) || !strings.Contains(context, "Goal") {
		t.Errorf("context %q", context)
	}
	data, _ := os.ReadFile(file)
	want := "* Approved plan\n:PROPERTIES:\n:SOURCE: /h/.claude/plans/x.md\n:END:\n#+begin_src markdown\n" +
		"# Add the loop\n\n,* a bullet\n,#+begin_src sh\n,,* escaped already\n#+end_src\n"
	if !strings.Contains(string(data), want) || !strings.Contains(string(data), ":SESSIONS: claude:s1\n") {
		t.Errorf("plan.org:\n%s\nwant:\n%s", data, want)
	}
	// The same session's next plan joins the first, from tool_input when
	// the response has none.
	hook, _ = json.Marshal(map[string]any{"session_id": "s1", "tool_name": "ExitPlanMode",
		"tool_input": map[string]string{"plan": "Second"}})
	if _, err := c.ImportClaude(hook, day); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(file)
	if strings.Count(string(data), "* Approved plan\n") != 2 || !strings.Contains(string(data), "Second\n#+end_src") {
		t.Errorf("plan.org after a second plan:\n%s", data)
	}
	if out, err := c.ImportClaude([]byte(`{"session_id":"s2","tool_name":"ExitPlanMode"}`), day); err != nil || len(out) != 0 {
		t.Errorf("no plan: %s, %v", out, err)
	}
}

func TestSessionHook(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	out, err := c.SessionHook([]byte(`{"session_id":"abc","source":"compact","cwd":"/nowhere"}`))
	if err != nil {
		t.Fatal(err)
	}
	if context := hookContext(t, out, "SessionStart"); !strings.Contains(context, filepath.Join(p.Dir, "plan.org")) {
		t.Errorf("context %q", context)
	}
	if out, err := c.SessionHook([]byte(`{"session_id":"zzz","source":"resume","cwd":"/nowhere"}`)); err != nil || len(out) != 0 {
		t.Errorf("no plan: %s, %v", out, err)
	}
}

func TestLoad(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	home, _ := os.UserHomeDir()
	if c, err := Load(); err != nil || c.Dir != filepath.Join(home, "git", "org", "agents") || c.Host == "" {
		t.Errorf("without a config: %+v, %v", c, err)
	}
	os.WriteFile(config, []byte(`{"repos": {}, "plans": {"dir": "/p", "host": "ngrok"}}`), 0o644)
	if c, err := Load(); err != nil || c.Dir != "/p" || c.Host != "ngrok" {
		t.Errorf("from the config: %+v, %v", c, err)
	}
}
