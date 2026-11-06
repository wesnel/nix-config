package loop

import (
	"errors"
	"io"
	"strings"
	"testing"
)

const replyFile = "\n=== thread bot1 fix resolve\n\nFixed in abc123:\n\n```go\n=== not a header\nfunc f() {\n\n\treturn\n}\n```\n\n" +
	"=== thread bot2 defer\nDeferred: out of scope.\n  indented\n\n\n=== thread done2 rebut resolve\nAlready done.\n"

func TestParseReplies(t *testing.T) {
	t.Run("sections", func(t *testing.T) {
		plan, err := ParseReplies(replyFile)
		if err != nil {
			t.Fatal(err)
		}
		want := []PlannedReply{
			{Thread: "bot1", Outcome: "fix", Resolve: true, Body: "Fixed in abc123:\n\n```go\n=== not a header\nfunc f() {\n\n\treturn\n}\n```"},
			{Thread: "bot2", Outcome: "defer", Body: "Deferred: out of scope.\n  indented"},
			{Thread: "done2", Outcome: "rebut", Resolve: true, Body: "Already done."},
		}
		if len(plan) != len(want) {
			t.Fatalf("plan: %+v", plan)
		}
		for i := range want {
			if plan[i] != want[i] {
				t.Errorf("section %d:\n got %+v\nwant %+v", i, plan[i], want[i])
			}
		}
	})

	t.Run("CRLF", func(t *testing.T) {
		if plan, err := ParseReplies("=== thread a fix\r\nbody\r\n"); err != nil || plan[0].Thread != "a" || plan[0].Outcome != "fix" {
			t.Errorf("CRLF: %+v, %v", plan, err)
		}
	})

	for name, text := range map[string]string{
		"no sections":        "just text\n",
		"empty":              "",
		"text before":        "hello\n=== thread a fix\nbody\n",
		"bad option":         "=== thread a fix resolved\nbody\n",
		"no thread":          "=== thread\nbody\n",
		"header in a body":   "=== thread a fix\n```\n=== thread b is here\n```\n",
		"empty body":         "=== thread a fix\n\n=== thread b fix\nbody\n",
		"empty last body":    "=== thread a fix\nbody\n=== thread b fix resolve\n\n",
		"duplicate":          "=== thread a fix\none\n=== thread a fix\ntwo\n",
		"no outcome":         "=== thread a\nbody\n",
		"resolve no outcome": "=== thread a resolve\nbody\n",
		"unknown outcome":    "=== thread a fixed\nbody\n",
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			if plan, err := ParseReplies(text); err == nil {
				t.Errorf("%s: accepted %+v", name, plan)
			}
		})
	}
	// The old header form is refused with the new one.
	if _, err := ParseReplies("=== thread a resolve\nbody\n"); err == nil || !strings.Contains(err.Error(), "=== thread THREAD OUTCOME") {
		t.Errorf("the old form's refusal: %v", err)
	}
}

func TestReplyAll(t *testing.T) {
	t.Run("replies and resolves", func(t *testing.T) {
		f := fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		plan, err := ParseReplies(replyFile)
		if err != nil {
			t.Fatal(err)
		}
		results, err := ReplyAll(plan, 1)
		if err != nil {
			t.Fatal(err)
		}
		want := []ReplyResult{
			{Thread: "bot1", PR: 1, URL: "https://x/bot1", Resolved: true},
			{Thread: "bot2", PR: 2, URL: "https://x/bot2"},
			{Thread: "done2", PR: 2, Resolved: true, Skipped: "already resolved"},
		}
		for i := range want {
			if results[i] != want[i] {
				t.Errorf("result %d:\n got %+v\nwant %+v", i, results[i], want[i])
			}
		}
		if !strings.Contains(f.replies["bot1"], "```go\n=== not a header") || len(f.replies) != 2 {
			t.Errorf("replies: %v", f.replies)
		}
		if strings.Join(f.resolved, ",") != "bot1" {
			t.Errorf("resolved: %v", f.resolved)
		}
	})

	// An agent outside a loop has no terminal, so it is refused, and told
	// what to hand the operator.
	t.Run("outside a bots-mode loop, with no terminal", func(t *testing.T) {
		f := fakeRepo(t)
		old := openTTY
		openTTY = func() (io.ReadWriteCloser, error) { return nil, errors.New("device not configured") }
		t.Cleanup(func() { openTTY = old })
		_, err := ReplyAll([]PlannedReply{{Thread: "bot1", Outcome: "fix", Body: "a"}}, 1)
		if !refused(err) || !strings.Contains(err.Error(), "pr-loop reply --from FILE --round") || len(f.replies) != 0 {
			t.Errorf("replied outside a loop: %v, %v", err, f.replies)
		}
	})

	// The operator, outside a loop, confirms each reply on the terminal,
	// in place of hand-built gh api calls; a reply not confirmed is not
	// posted.
	t.Run("outside a bots-mode loop, on the operator's terminal", func(t *testing.T) {
		f := fakeRepo(t)
		tty := useTTY(t, "y\nn\n")
		results, err := ReplyAll([]PlannedReply{{Thread: "bot1", Outcome: "fix", Body: "Fixed in \"Check it\".", Resolve: true},
			{Thread: "bot2", Outcome: "defer", Body: "Later."}}, 2)
		if err != nil {
			t.Fatal(err)
		}
		want := []ReplyResult{
			{Thread: "bot1", PR: 1, URL: "https://x/bot1", Resolved: true},
			{Thread: "bot2", PR: 2, Skipped: "not confirmed"},
		}
		for i := range want {
			if i >= len(results) || results[i] != want[i] {
				t.Errorf("results %+v, want %+v", results, want)
				break
			}
		}
		if len(f.replies) != 1 || strings.Join(f.resolved, ",") != "bot1" {
			t.Errorf("posted %v, resolved %v", f.replies, f.resolved)
		}
		if shown := tty.shown.String(); !strings.Contains(shown, "Fixed in \"Check it\".") || !strings.Contains(shown, "answered fix") ||
			!strings.Contains(shown, "Later.") {
			t.Errorf("shown:\n%s", shown)
		}
	})

	t.Run("a round below 1", func(t *testing.T) {
		f := fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		if _, err := ReplyAll([]PlannedReply{{Thread: "bot1", Outcome: "fix", Body: "a"}}, 0); !refused(err) || len(f.replies) != 0 {
			t.Errorf("round 0: %v, %v", err, f.replies)
		}
	})

	t.Run("checks every thread first", func(t *testing.T) {
		f := fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		plan := []PlannedReply{{Thread: "bot1", Body: "a"}, {Thread: "theirs", Body: "b"}, {Thread: "bot2", Body: "c"}, {Thread: "closed", Body: "d"}}
		_, err := ReplyAll(plan, 1)
		if !refused(err) || !strings.Contains(err.Error(), "#3") || !strings.Contains(err.Error(), "#4") {
			t.Errorf("refusal should name every bad thread: %v", err)
		}
		_, err = ReplyAll([]PlannedReply{{Thread: "bot1", Body: "a"}, {Thread: "missing", Body: "b"}, {Thread: "theirs", Body: "c"}}, 1)
		if err == nil || !strings.Contains(err.Error(), "missing:") || !strings.Contains(err.Error(), "theirs:") {
			t.Errorf("an unknown thread should be reported with the rest: %v", err)
		}
		if len(f.replies) != 0 || len(f.resolved) != 0 {
			t.Errorf("posted before checking: %v %v", f.replies, f.resolved)
		}
	})

	t.Run("a reply file naming a person's thread", func(t *testing.T) {
		f := fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		if _, err := ReplyAll([]PlannedReply{{Thread: "bot1", Body: "a"}, {Thread: "human1", Body: "b"}}, 1); !refused(err) {
			t.Errorf("a reply file reached a person: %v", err)
		}
		if len(f.replies) != 0 {
			t.Errorf("posted: %v", f.replies)
		}
	})

	t.Run("reports where it stopped", func(t *testing.T) {
		f := fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		f.failReply = "bot2"
		_, err := ReplyAll([]PlannedReply{{Thread: "bot1", Body: "a", Resolve: true}, {Thread: "bot2", Body: "b"}, {Thread: "answered1", Body: "c"}}, 1)
		if err == nil || !strings.Contains(err.Error(), "bot2") || !strings.Contains(err.Error(), `"thread":"bot1"`) {
			t.Errorf("error should name the failed thread and what was done: %v", err)
		}
		if _, ok := f.replies["answered1"]; ok {
			t.Error("carried on after a failure")
		}
	})
}
