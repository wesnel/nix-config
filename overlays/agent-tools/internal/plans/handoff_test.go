package plans

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Records shaped as the harnesses write them: Claude Code 2.1.325's
// transcript lines for a line the operator ran with !, and Codex's rollout
// lines for one.
const (
	claudePrompt   = `{"type":"user","isSidechain":false,"message":{"role":"user","content":"Hand me the cleanup"}}`
	claudeReply    = `{"type":"assistant","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"Run it."}]}}`
	claudeToolCall = `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
	claudeStopHook = `{"type":"system","subtype":"stop_hook_summary","hookCount":1}`
	codexReply     = `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Run it."}]}}`
	codexPrompt    = `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"ok"}]}}`
)

func claudeBang(command, stderr string) string {
	input, _ := json.Marshal(map[string]any{"type": "user", "isSidechain": false,
		"message": map[string]any{"role": "user", "content": "<bash-input>" + command + "</bash-input>"}})
	output, _ := json.Marshal(map[string]any{"type": "user", "isSidechain": false,
		"message": map[string]any{"role": "user", "content": "<bash-stdout></bash-stdout><bash-stderr>" + stderr + "</bash-stderr>"}})
	return string(input) + "\n" + string(output)
}

func codexBang(command string, exit int) string {
	text := "<user_shell_command>\n<command>\n" + command + "\n</command>\n<result>\nExit code: " +
		strconv.Itoa(exit) + "\nDuration: 0.1 seconds\nOutput:\nzsh: command not found\n\n</result>\n</user_shell_command>"
	data, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": text}}}})
	return string(data)
}

func transcript(t *testing.T, lines ...string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// TestHandoffs covers how remind meets a line the operator pasted with !
// that may have failed: Codex's next UserPromptSubmit files a retro
// question for each one with a non-zero exit code since the agent last
// spoke; Claude Code, which records no exit code, has its Stop hook ask the
// agent once, after its reply to a line that wrote to stderr.
func TestHandoffs(t *testing.T) {
	t.Parallel()
	codexEvent := func(file string) []byte {
		data, _ := json.Marshal(map[string]any{"session_id": "abc", "transcript_path": file, "cwd": "/nowhere",
			"hook_event_name": "UserPromptSubmit", "prompt": "ok", "turn_id": "t2"})
		return data
	}
	stopEvent := func(file string, active bool) []byte {
		data, _ := json.Marshal(map[string]any{"session_id": "abc", "transcript_path": file, "cwd": "/nowhere",
			"hook_event_name": "Stop", "stop_hook_active": active})
		return data
	}
	retros := func(t *testing.T, p Plan) []Question {
		t.Helper()
		qs, err := p.Questions(true)
		if err != nil {
			t.Fatal(err)
		}
		var out []Question
		for _, q := range qs {
			if q.Kind == "retro" {
				out = append(out, q)
			}
		}
		return out
	}

	t.Run("codex files a failed line", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		file := transcript(t, codexReply, codexBang("git sign-pass --push", 0), codexBang("trash /w/a /w/b", 127), codexPrompt)
		out := string(c.Remind(codexEvent(file), "codex"))
		qs := retros(t, p)
		if len(qs) != 1 {
			t.Fatalf("filed %d retro questions, want 1: %+v", len(qs), qs)
		}
		// The rollout's line number of the failed line, the third.
		if !strings.Contains(qs[0].Context, file+":3") || !strings.Contains(qs[0].Context, "127") {
			t.Errorf("context %q, want where the failed line is and its exit code", qs[0].Context)
		}
		if !strings.Contains(out, qs[0].ID) || !strings.Contains(out, "trash /w/a /w/b") || strings.Contains(out, "git sign-pass") ||
			!strings.Contains(out, "UserPromptSubmit") {
			t.Errorf("printed %q, want UserPromptSubmit context naming %s and the failed line alone", out, qs[0].ID)
		}
	})
	// The plan is pushed to the notes repository, and nobody reads the
	// line before the hook files it, so a secret on it must stay out.
	t.Run("codex keeps the line itself out of the plan", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		c.Remind(codexEvent(transcript(t, codexReply, codexBang("GITHUB_TOKEN=ghp_s3cret gh api user", 1), codexPrompt)), "codex")
		if qs := retros(t, p); len(qs) != 1 {
			t.Fatalf("filed %d retro questions, want 1: %+v", len(qs), qs)
		}
		data, err := os.ReadFile(p.File())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "ghp_s3cret") {
			t.Errorf("the plan holds the line's secret:\n%s", data)
		}
	})
	t.Run("codex leaves a line the agent already answered", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		out := string(c.Remind(codexEvent(transcript(t, codexBang("trash /w/a", 1), codexReply, codexPrompt)), "codex"))
		if qs := retros(t, p); len(qs) != 0 || strings.Contains(out, "trash") {
			t.Errorf("filed %+v and printed %q for a line before the agent's reply", qs, out)
		}
	})
	// A turn aborted before the agent spoke leaves the line after its last
	// message, so the next prompt sees it again.
	t.Run("codex files a line once", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		file := transcript(t, codexReply, codexBang("trash /w/a", 1), codexPrompt)
		c.Remind(codexEvent(file), "codex")
		out := string(c.Remind(codexEvent(file), "codex"))
		if qs := retros(t, p); len(qs) != 1 || strings.Contains(out, "trash") {
			t.Errorf("filed %d retro questions and printed %q on the second prompt, want 1 and nothing of it", len(qs), out)
		}
	})
	t.Run("codex files a line once though its question is settled", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		file := transcript(t, codexReply, codexBang("trash /w/a", 1), codexPrompt)
		c.Remind(codexEvent(file), "codex")
		qs := retros(t, p)
		if len(qs) != 1 {
			t.Fatalf("filed %d retro questions, want 1", len(qs))
		}
		if _, err := p.Decide(qs[0].ID, DecideOptions{Answer: "replaced", Drop: true}, day); err != nil {
			t.Fatal(err)
		}
		if out := string(c.Remind(codexEvent(file), "codex")); strings.Contains(out, "trash") || len(retros(t, p)) != 0 {
			t.Errorf("filed the line again after its question was dropped: printed %q", out)
		}
	})
	t.Run("a harness reads only its own transcript", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		codex := transcript(t, codexReply, codexBang("trash /w/a", 1), codexPrompt)
		claude := transcript(t, claudeBang("trash /w/a", "boom\n"), claudeReply)
		prompt := string(c.Remind(codexEvent(codex), "claude"))
		stop := string(c.Remind(stopEvent(claude, false), "codex"))
		if qs := retros(t, p); len(qs) != 0 || strings.Contains(prompt, "trash") || stop != "" {
			t.Errorf("filed %+v, printed %q on a Claude Code prompt and %q on a Codex Stop", qs, prompt, stop)
		}
	})
	// A sign-pass handed over with --notify reports its own result, a
	// refusal included, to the session.
	t.Run("a line that reports its own result", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		line := "git sign-pass --push --notify 20261116-x.box.ab12 wgn/loop"
		prompt := string(c.Remind(codexEvent(transcript(t, codexReply, codexBang(line, 1), codexPrompt)), "codex"))
		stop := string(c.Remind(stopEvent(transcript(t, claudeBang(line, "git-sign-pass: refused: x\n"), claudeReply), false), "claude"))
		if qs := retros(t, p); len(qs) != 0 || strings.Contains(prompt, "sign-pass") || stop != "" {
			t.Errorf("filed %+v, printed %q on a Codex prompt and %q on a Claude Code Stop", qs, prompt, stop)
		}
	})
	t.Run("codex without a plan asks the agent", func(t *testing.T) {
		c := testConfig(t)
		out := string(c.Remind(codexEvent(transcript(t, codexReply, codexBang("trash /w/a", 1), codexPrompt)), "codex"))
		if !strings.Contains(out, "trash /w/a") || !strings.Contains(out, "retro") {
			t.Errorf("printed %q, want the failed line and the retro skill", out)
		}
	})

	stop := func(t *testing.T, lines ...string) (out string, p Plan) {
		t.Helper()
		c := testConfig(t)
		p = newPlan(t, c, "x")
		return string(c.Remind(stopEvent(transcript(t, lines...), false), "claude")), p
	}
	t.Run("claude stop after a line with stderr blocks once", func(t *testing.T) {
		out, p := stop(t, claudePrompt, claudeReply, claudeBang("trash /w/a && git -C /w branch -D x", "error: branch not found\n"), claudeReply, claudeStopHook)
		var got struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if json.Unmarshal([]byte(out), &got) != nil || got.Decision != "block" {
			t.Fatalf("printed %q, want a block decision", out)
		}
		for _, w := range []string{"trash /w/a && git -C /w branch -D x", "--kind retro", p.File(), "<REDACTED>"} {
			if !strings.Contains(got.Reason, w) {
				t.Errorf("reason %q lacks %q", got.Reason, w)
			}
		}
		if qs := retros(t, p); len(qs) != 0 {
			t.Errorf("filed %+v itself; the agent judges in Claude Code", qs)
		}
	})
	t.Run("claude stop without a plan", func(t *testing.T) {
		c := testConfig(t)
		out := string(c.Remind(stopEvent(transcript(t, claudeBang("trash /w/a", "boom\n"), claudeReply), false), "claude"))
		if !strings.Contains(out, "agent-plan ask PLAN --kind retro") {
			t.Errorf("printed %q, want the command with PLAN in its place", out)
		}
	})
	t.Run("claude stop after a line with empty stderr", func(t *testing.T) {
		if out, _ := stop(t, claudeReply, claudeBang("trash /w/a", ""), claudeReply); out != "" {
			t.Errorf("printed %q", out)
		}
	})
	t.Run("claude stop after a prompt that followed the line", func(t *testing.T) {
		if out, _ := stop(t, claudeBang("trash /w/a", "boom\n"), claudeReply, claudePrompt, claudeReply, claudeToolCall, claudeReply); out != "" {
			t.Errorf("printed %q", out)
		}
	})
	t.Run("claude stop already continued once", func(t *testing.T) {
		c := testConfig(t)
		newPlan(t, c, "x")
		file := transcript(t, claudeBang("trash /w/a", "boom\n"), claudeReply)
		if out := string(c.Remind(stopEvent(file, true), "claude")); out != "" {
			t.Errorf("printed %q", out)
		}
		if out := string(c.Remind(stopEvent(file, false), "claude")); !strings.Contains(out, "block") {
			t.Errorf("printed %q without stop_hook_active, want a block", out)
		}
	})
	// A line over the limit stops the scan partway: what it read is not
	// the transcript's end, so neither harness acts on it.
	t.Run("a transcript read partway is not acted on", func(t *testing.T) {
		// The agent's reply in Codex, which Claude Code's reader skips.
		long := strings.Replace(codexReply, "Run it.", strings.Repeat("x", 2000), 1)
		codex := transcript(t, codexReply, codexBang("trash /w/a", 1), long)
		claude := transcript(t, claudeBang("trash /w/a", "boom\n"), long, claudePrompt, claudeReply)
		if got := codexFailedLines(codex, 1000); len(got) != 0 {
			t.Errorf("codex: %+v from a partial read", got)
		}
		if got, ok := claudeAnsweredLine(claude, 1000); ok {
			t.Errorf("claude: %+v from a partial read", got)
		}
		if got := codexFailedLines(codex, 4000); len(got) != 0 {
			t.Errorf("codex: %+v though the long line answered it", got)
		}
		if got, ok := claudeAnsweredLine(transcript(t, claudeBang("trash /w/a", "boom\n"), long), 4000); !ok {
			t.Errorf("claude: %+v, want the line read whole", got)
		}
	})
}
