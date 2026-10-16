package watch

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// token is shaped as a GitHub token, for the redaction to find.
var token = "ghp_" + strings.Repeat("Fa9", 12)

// fixture is testdata/session.jsonl, a session's stream-json log shaped as
// Claude Code writes it, with token in place of @TOKEN@.
func fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(data, []byte("@TOKEN@"), []byte(token))
}

const want = `== session 0a1b2c3d started in /work/repo (claude-test)
>> Run the review loop on feature-a.
Starting the loop on #42.
CI first.
-> Bash: pr-loop wait ci 42
<- Bash: {
   "result": "passed", [+2 lines]
-> Agent: pr-owner: Triage PR #42 bot threads
++ subagent started: Triage PR #42 bot threads
[Triage PR #42 bot threads] -> Edit: /work/repo/.pr-loop/42/main.go
[Triage PR #42 bot threads] <- Edit: The file /work/repo/.pr-loop/42/main.go has been updated successfully.
[Triage PR #42 bot threads] -> Bash: pr-loop reply T_1 --body-file - ...
[Triage PR #42 bot threads] !! REFUSED Bash: pr-loop: refused: T_1 is a thread a person started; the loop does not reply to people
[Triage PR #42 bot threads] -> Bash: env | grep TOKEN
[Triage PR #42 bot threads] <- Bash: FAKE_TOKEN=[REDACTED]
[Triage PR #42 bot threads]    [REDACTED GitHub token] alone
[Triage PR #42 bot threads] -> Bash: git push origin feature-a
[Triage PR #42 bot threads] !! REFUSED Bash: Permission for this action was denied by the Claude Code auto mode classifier. Reason: pushing is pr-loop's.
-- subagent completed: Triage PR #42 bot threads: Two threads triaged; one needs the operator.
<- Agent: Report: two threads triaged.
   One needs the operator. [+2 lines]
== result success after 5 turns, 61s, $1.25: The loop stopped: a thread needs the operator.
   See report.md.
`

func TestFormat(t *testing.T) {
	var out bytes.Buffer
	f := NewFormatter(&out)
	for _, line := range bytes.SplitAfter(fixture(t), []byte("\n")) {
		if len(line) > 0 {
			f.Event(line)
		}
	}
	if out.String() != want {
		t.Errorf("formatted log:\n%s\nwant:\n%s", out.String(), want)
	}
	if strings.Contains(out.String(), token) {
		t.Error("a token reached the output")
	}

	t.Run("a line that is not JSON", func(t *testing.T) {
		var out bytes.Buffer
		NewFormatter(&out).Event([]byte("not json " + token + "\n"))
		if got := out.String(); got != "?? a line that is not stream-json\n" {
			t.Errorf("got %q", got)
		}
	})

	// The log holds what commands, bots and the sandboxed session wrote:
	// terminal control sequences in it would act on the operator's
	// terminal, and a secret can sit in any field printed.
	t.Run("control sequences and secrets in every field", func(t *testing.T) {
		var out bytes.Buffer
		f := NewFormatter(&out)
		esc := `\u001b`
		for _, line := range []string{
			`{"type":"system","subtype":"init","cwd":"/w` + esc + `]52;c;aGk=\u0007 ` + token + `","model":"m` + esc + `[2J","session_id":"s"}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"hi ` + esc + `]0;pwned\u0007 there\r\u009b31m"}]},"parent_tool_use_id":null}`,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]},` +
				`"parent_tool_use_id":"a","task_description":"Task ` + esc + `[31m ` + token + `"}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"` + esc + `]8;;https://evil.example/\u0007link"}]},"parent_tool_use_id":null}`,
		} {
			f.Event([]byte(line))
		}
		got := out.String()
		if strings.ContainsAny(got, "\x1b\x07\r\u009b") {
			t.Errorf("a control character reached the output: %q", got)
		}
		if strings.Contains(got, token) {
			t.Errorf("a token reached the output: %q", got)
		}
		if !strings.Contains(got, "^[]0;pwned^G") {
			t.Errorf("a control sequence is not shown as text: %q", got)
		}
	})

	t.Run("a long line", func(t *testing.T) {
		var out bytes.Buffer
		long := strings.Repeat("x", 500)
		NewFormatter(&out).Event([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t","name":"Bash","input":{"command":"echo ` + long + `"}}]},"parent_tool_use_id":null}`))
		if got := strings.TrimSuffix(out.String(), "\n"); len([]rune(got)) > 200 || !strings.HasSuffix(got, "...") {
			t.Errorf("a long command was not shortened: %q", got)
		}
	})
}

// syncBuffer is a bytes.Buffer safe for one writer and one reader.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// eventually waits for the output to satisfy ok.
func eventually(t *testing.T, out *syncBuffer, what string, ok func(string) bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(out.String()); {
		if time.Now().After(deadline) {
			t.Fatalf("%s; output:\n%s", what, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

const (
	textEvent = `{"type":"assistant","message":{"content":[{"type":"text","text":"%s"}]},"parent_tool_use_id":null}` + "\n"
)

func text(s string) string { return strings.Replace(textEvent, "%s", s, 1) }

func TestFollow(t *testing.T) {
	// A key's body cut over several stderr lines is redacted as a whole: its
	// short last line alone has no shape to find.
	t.Run("the error log redacted as a whole", func(t *testing.T) {
		dir := t.TempDir()
		log := filepath.Join(dir, "pr-loop-session.jsonl")
		line := strings.Repeat("Fa9+", 16)
		appendTo(t, filepath.Join(dir, "pr-loop-session.err"), line+"\n"+line+"\nFa9+/w==\n\x1b[2Jdone\n")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		out := &syncBuffer{}
		go Follow(ctx, log, true, out, 10*time.Millisecond)
		eventually(t, out, "the error log is not shown", func(s string) bool { return strings.Contains(s, "done\n") })
		if got := out.String(); strings.Contains(got, "Fa9+") || strings.Contains(got, "\x1b") {
			t.Errorf("a key line or a control character reached the output:\n%q", got)
		}
	})

	t.Run("from the start, as the log grows", func(t *testing.T) {
		dir := t.TempDir()
		log := filepath.Join(dir, "pr-loop-session.jsonl")
		second := text("second")
		appendTo(t, log, text("first")+second[:20])
		appendTo(t, filepath.Join(dir, "pr-loop-session.err"), "Background tasks still running "+token+"\n")
		ctx, cancel := context.WithCancel(context.Background())
		out := &syncBuffer{}
		done := make(chan error)
		go func() { done <- Follow(ctx, log, true, out, 10*time.Millisecond) }()

		eventually(t, out, "the first event is not shown", func(s string) bool { return strings.Contains(s, "first\n") })
		eventually(t, out, "the error log is not shown", func(s string) bool {
			return strings.Contains(s, "stderr: Background tasks still running [REDACTED GitHub token]\n")
		})
		time.Sleep(50 * time.Millisecond)
		if strings.Contains(out.String(), "?? ") || strings.Contains(out.String(), "second") {
			t.Fatalf("a half-written line was read:\n%s", out.String())
		}
		appendTo(t, log, second[20:])
		eventually(t, out, "the completed line is not shown", func(s string) bool { return strings.Contains(s, "second\n") })

		// A new session truncates the log and starts it again.
		if err := os.WriteFile(log, []byte(text("again")), 0o600); err != nil {
			t.Fatal(err)
		}
		eventually(t, out, "the restarted log is not shown", func(s string) bool { return strings.Contains(s, "again\n") })

		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("follow: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("follow did not stop when cancelled")
		}
		if strings.Contains(out.String(), token) {
			t.Error("a token reached the output")
		}
	})

	t.Run("from the end, for a log not yet written", func(t *testing.T) {
		dir := t.TempDir()
		log := filepath.Join(dir, "pr-loop-session.jsonl")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		out := &syncBuffer{}
		go Follow(ctx, log, false, out, 10*time.Millisecond)
		time.Sleep(50 * time.Millisecond)
		appendTo(t, log, text("new"))
		eventually(t, out, "a log created later is not followed", func(s string) bool { return strings.Contains(s, "new\n") })
	})

	t.Run("from the end", func(t *testing.T) {
		dir := t.TempDir()
		log := filepath.Join(dir, "pr-loop-session.jsonl")
		half := text("half")
		appendTo(t, log, text("old")+half[:15])
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		out := &syncBuffer{}
		go Follow(ctx, log, false, out, 10*time.Millisecond)
		time.Sleep(50 * time.Millisecond)
		appendTo(t, log, half[15:]+text("new"))
		eventually(t, out, "a later event is not shown", func(s string) bool { return strings.Contains(s, "new\n") })
		if got := out.String(); strings.Contains(got, "old") || strings.Contains(got, "half") || strings.Contains(got, "??") {
			t.Errorf("events before the start were shown:\n%s", got)
		}
	})
}
