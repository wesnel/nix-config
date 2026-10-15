package loop

import (
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/relay"
)

const replyFile = "\n=== thread human1 resolve\n\nFixed in abc123:\n\n```go\n=== not a header\nfunc f() {\n\n\treturn\n}\n```\n\n" +
	"=== thread human2\nDeferred: out of scope.\n  indented\n\n\n=== thread done2 resolve\nAlready done.\n"

func TestParseReplies(t *testing.T) {
	plan, err := ParseReplies(replyFile)
	if err != nil {
		t.Fatal(err)
	}
	want := []PlannedReply{
		{Thread: "human1", Resolve: true, Body: "Fixed in abc123:\n\n```go\n=== not a header\nfunc f() {\n\n\treturn\n}\n```"},
		{Thread: "human2", Body: "Deferred: out of scope.\n  indented"},
		{Thread: "done2", Resolve: true, Body: "Already done."},
	}
	if len(plan) != len(want) {
		t.Fatalf("plan: %+v", plan)
	}
	for i := range want {
		if plan[i] != want[i] {
			t.Errorf("section %d:\n got %+v\nwant %+v", i, plan[i], want[i])
		}
	}
	if plan, err := ParseReplies("=== thread a\r\nbody\r\n"); err != nil || plan[0].Thread != "a" {
		t.Errorf("CRLF: %+v, %v", plan, err)
	}
	for name, text := range map[string]string{
		"no sections":      "just text\n",
		"empty":            "",
		"text before":      "hello\n=== thread a\nbody\n",
		"bad option":       "=== thread a resolved\nbody\n",
		"no thread":        "=== thread\nbody\n",
		"header in a body": "=== thread a\n```\n=== thread b is here\n```\n",
		"empty body":       "=== thread a\n\n=== thread b\nbody\n",
		"empty last body":  "=== thread a\nbody\n=== thread b resolve\n\n",
		"duplicate":        "=== thread a\none\n=== thread a\ntwo\n",
	} {
		if plan, err := ParseReplies(text); err == nil {
			t.Errorf("%s: accepted %+v", name, plan)
		}
	}
}

func TestReplyAll(t *testing.T) {
	f := fakeRepo(t)
	plan, err := ParseReplies(replyFile)
	if err != nil {
		t.Fatal(err)
	}
	results, err := ReplyAll(plan)
	if err != nil {
		t.Fatal(err)
	}
	want := []ReplyResult{
		{Thread: "human1", PR: 1, URL: "https://x/human1", Resolved: true},
		{Thread: "human2", PR: 2, URL: "https://x/human2"},
		{Thread: "done2", PR: 2, Resolved: true, Skipped: "already resolved"},
	}
	for i := range want {
		if results[i] != want[i] {
			t.Errorf("result %d:\n got %+v\nwant %+v", i, results[i], want[i])
		}
	}
	if !strings.Contains(f.replies["human1"], "```go\n=== not a header") || len(f.replies) != 2 {
		t.Errorf("replies: %v", f.replies)
	}
	if strings.Join(f.resolved, ",") != "human1" {
		t.Errorf("resolved: %v", f.resolved)
	}
}

func TestReplyAllChecksEveryThreadFirst(t *testing.T) {
	f := fakeRepo(t)
	plan := []PlannedReply{{Thread: "human1", Body: "a"}, {Thread: "theirs", Body: "b"}, {Thread: "human2", Body: "c"}, {Thread: "closed", Body: "d"}}
	_, err := ReplyAll(plan)
	if !refused(err) || !strings.Contains(err.Error(), "#3") || !strings.Contains(err.Error(), "#4") {
		t.Errorf("refusal should name every bad thread: %v", err)
	}
	_, err = ReplyAll([]PlannedReply{{Thread: "human1", Body: "a"}, {Thread: "missing", Body: "b"}, {Thread: "theirs", Body: "c"}})
	if err == nil || !strings.Contains(err.Error(), "missing:") || !strings.Contains(err.Error(), "theirs:") {
		t.Errorf("an unknown thread should be reported with the rest: %v", err)
	}
	if len(f.replies) != 0 || len(f.resolved) != 0 {
		t.Errorf("posted before checking: %v %v", f.replies, f.resolved)
	}

	t.Setenv(relay.RelayEnv, "1")
	registerLoop(t)
	if _, err := ReplyAll([]PlannedReply{{Thread: "bot1", Body: "a"}, {Thread: "human1", Body: "b"}}); !refused(err) {
		t.Errorf("a loop's reply file reached a person: %v", err)
	}
	if len(f.replies) != 0 {
		t.Errorf("posted: %v", f.replies)
	}
}

func TestReplyAllReportsWhereItStopped(t *testing.T) {
	f := fakeRepo(t)
	f.failReply = "human2"
	_, err := ReplyAll([]PlannedReply{{Thread: "human1", Body: "a", Resolve: true}, {Thread: "human2", Body: "b"}, {Thread: "answered", Body: "c"}})
	if err == nil || !strings.Contains(err.Error(), "human2") || !strings.Contains(err.Error(), `"thread":"human1"`) {
		t.Errorf("error should name the failed thread and what was done: %v", err)
	}
	if _, ok := f.replies["answered"]; ok {
		t.Error("carried on after a failure")
	}
}
