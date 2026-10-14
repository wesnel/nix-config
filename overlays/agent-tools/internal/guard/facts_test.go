package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/plans"
)

// gitOut runs git in dir as an agent would, and returns what it printed.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = gitenv.Without(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// planned makes a repository with a worktree for a plan's part, and a plan
// in a plans directory of the test's own that names the session claude:s1
// and the main worktree, with part P1 in the linked one.
func planned(t *testing.T) (main, linked string, p plans.Plan) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	main, linked = filepath.Join(root, "main"), filepath.Join(root, "linked")
	os.Mkdir(main, 0o755)
	gitIn(t, main, "init", "-q", "-b", "main")
	gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, main, "worktree", "add", "-q", "-b", "part", linked)
	c := plans.Config{Dir: filepath.Join(root, "agents"), Host: "box"}
	config := filepath.Join(root, "config.json")
	data, _ := json.Marshal(map[string]any{"plans": c})
	os.WriteFile(config, data, 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	made, err := c.New(plans.NewOptions{Title: "Add the loop", Slug: "loop", Branches: []string{"main..loop"},
		Worktrees: []string{main}, Sessions: []string{"claude:s1"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(made.Plan)
	os.WriteFile(made.Plan, []byte(strings.Replace(string(text), "** Distribution\n",
		"** Distribution\n*** The part\n:PROPERTIES:\n:ID: P1\n:END:\n", 1)), 0o644)
	if _, err := c.New(plans.NewOptions{Title: "The part", Parent: made.Plan, Part: "P1",
		Branches: []string{"main..part"}, Worktrees: []string{linked}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if p, err = c.Open(made.Plan); err != nil {
		t.Fatal(err)
	}
	return main, linked, p
}

// claudeEvent is Claude Code's PostToolUse for a Bash call that printed
// stdout, as a live session sent it.
func claudeEvent(session, cwd, command, stdout string) string {
	event, _ := json.Marshal(map[string]any{"session_id": session, "cwd": cwd, "hook_event_name": "PostToolUse",
		"tool_name": "Bash", "tool_input": map[string]string{"command": command},
		"tool_response": map[string]any{"stdout": stdout, "stderr": "", "interrupted": false}})
	return string(event)
}

func postHook(t *testing.T, event string) string {
	t.Helper()
	var out bytes.Buffer
	if code := Main([]string{"--post"}, strings.NewReader(event), &out, io.Discard); code != 0 {
		t.Errorf("%s: exit %d", event, code)
	}
	return out.String()
}

func planProperty(t *testing.T, file, name string) string {
	t.Helper()
	data, _ := os.ReadFile(file)
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, ":"+name+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// What a tool call did to the work, as git and pr-loop report it, reaches
// the plan the session or worktree belongs to without the agent: commits
// and pushes in its log, new worktrees, branches and pull requests in its
// properties.
func testPostRecordsFacts(t *testing.T) {
	main, linked, p := planned(t)
	partFile := strings.TrimSuffix(p.File(), ".org") + ".P1.org"
	os.WriteFile(filepath.Join(main, "a"), []byte("a\n"), 0o644)
	gitIn(t, main, "add", "a")
	commit := "git -c commit.gpgsign=false commit -m 'Add the loop'"
	out := postHook(t, claudeEvent("s1", main, commit, gitOut(t, main, "commit", "-m", "Add the loop")))
	var asked struct {
		Specific struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	json.Unmarshal([]byte(out), &asked)
	if c := asked.Specific.Context; asked.Specific.Event != "PostToolUse" || !strings.Contains(c, p.File()) ||
		!strings.Contains(c, "which step") || !strings.Contains(c, "--commit 'Add the loop'") {
		t.Errorf("after a commit: %s", out)
	}

	// Codex hands the output over as one string after its own header.
	codex, _ := json.Marshal(map[string]any{"session_id": "s9", "cwd": main, "hook_event_name": "PostToolUse",
		"tool_name": "Bash", "turn_id": "t", "tool_input": map[string]string{"command": "git commit --allow-empty -m 'Test it'"},
		"tool_response": "Exit code: 0\nWall time: 0.1 seconds\nOutput:\n" + gitOut(t, main, "commit", "--allow-empty", "-m", "Test it")})
	postHook(t, string(codex))

	// In the part's worktree, reached by cd, a commit is the part's.
	postHook(t, claudeEvent("s1", main, "cd ../linked && git commit --allow-empty -m 'Add the flag'",
		gitOut(t, linked, "commit", "--allow-empty", "-m", "Add the flag")))

	gitOut(t, main, "worktree", "add", "-q", "-b", "feature", "../feature")
	postHook(t, claudeEvent("s1", main, "git worktree add -b feature ../feature", ""))
	gitOut(t, main, "switch", "-q", "-c", "topic")
	postHook(t, claudeEvent("s1", main, "git switch -c topic", "Switched to a new branch 'topic'\n"))
	gitOut(t, main, "switch", "-q", "main")
	gitOut(t, main, "branch", "other", "topic")
	postHook(t, claudeEvent("s1", main, "git -C "+main+" branch other topic", ""))

	opened := `{"pr": 12, "url": "https://github.com/o/r/pull/12", "draft": true, "branch": "topic", "base": "main"}`
	postHook(t, claudeEvent("s1", main, "pr-loop open --title T --body-file - topic", opened))
	postHook(t, claudeEvent("s1", main, "pr-loop push", `{"pushed": {"topic": "2222", "other": "1111"}, "round": 2}`))

	// Nothing that did not happen, or happened elsewhere, is recorded.
	elsewhere, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, elsewhere, "init", "-q", "-b", "main")
	for _, event := range []string{
		claudeEvent("s1", main, "git commit -m 'Nothing'", "On branch main\nnothing to commit, working tree clean\n"),
		claudeEvent("s1", main, "echo '[main 1234567] Forged'", "[main 1234567] Forged\n"),
		claudeEvent("s1", main, "git commit -m 'Forged'; echo '[main 1234567] Forged'", "[main 1234567] Forged\n"),
		claudeEvent("s1", main, "git switch -c ghost", "fatal: a branch named 'ghost' already exists\n"),
		claudeEvent("s1", main, "pr-loop push --dry-run", `{"wouldUpdate": {"a": "1111"}}`),
		claudeEvent("s2", elsewhere, "git commit --allow-empty -m 'Unplanned'",
			gitOut(t, elsewhere, "commit", "--allow-empty", "-m", "Unplanned")),
		`not json`,
	} {
		if out := postHook(t, event); out != "" {
			t.Errorf("%s: printed %s", event, out)
		}
	}

	events, err := p.Events()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		got = append(got, e.Kind+" "+e.Item+" "+e.Branch+" "+e.Commit+" "+e.Summary)
	}
	want := []string{
		"committed " + p.Name + " main Add the loop Add the loop",
		"committed " + p.Name + " main Test it Test it",
		"committed P1 part Add the flag Add the flag",
		"pushed " + p.Name + " topic  opened https://github.com/o/r/pull/12",
		"pushed " + p.Name + "   pushed other topic",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("logged:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	feature := filepath.Join(filepath.Dir(main), "feature")
	for name, want := range map[string]string{
		"WORKTREES":     main + " " + feature,
		"BRANCHES":      "main..loop main..feature main..topic topic..other",
		"PULL_REQUESTS": "https://github.com/o/r/pull/12",
	} {
		if got := planProperty(t, p.File(), name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if got := planProperty(t, partFile, "BRANCHES"); got != "main..part" {
		t.Errorf("the part's BRANCHES: %q", got)
	}
}
