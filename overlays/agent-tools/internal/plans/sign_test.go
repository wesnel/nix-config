package plans

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// A sign-pass handed over in a session is requested in the session's plan,
// under an ID that names the plan, and refused where there is no plan.
func TestRequestSign(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	id, err := c.RequestSign("abc", "", []string{"loop", "flag"}, day)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, p.Name+".") || !signID.MatchString(id) {
		t.Errorf("ID %q does not name plan %s", id, p.Name)
	}
	again, _ := c.RequestSign("abc", "", nil, day)
	if again == id {
		t.Errorf("two requests share ID %q", id)
	}
	log, _ := os.ReadFile(p.log())
	for _, want := range []string{"* sign-requested " + p.Name + " — " + id, ":SIGN: " + id, ":SESSION: abc", ":BRANCH: loop,flag"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}

	_, err = c.RequestSign("zzz", "/elsewhere", nil, day)
	isRefusal(t, "a session with no plan", err)
	_, err = c.RequestSign("", "/w/loop", nil, day)
	isRefusal(t, "no session", err)
}

// A result is logged in the plan that holds its request, and only there.
func TestRecordSign(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	id, _ := c.RequestSign("abc", "", []string{"loop"}, day)
	file, err := c.RecordSign(id, SignResult{Exit: 2, Summary: "this worktree has uncommitted changes"}, day)
	if err != nil || file != p.File() {
		t.Fatalf("record: %q %v", file, err)
	}
	events, _ := p.Events()
	last := events[len(events)-1]
	if last.Kind != "sign-refused" || last.Sign != id || last.Exit != "2" || last.Session != "abc" ||
		last.Summary != "this worktree has uncommitted changes" {
		t.Errorf("logged %+v", last)
	}

	for _, bad := range []string{p.Name + ".0badc0de", "2026-01-01-gone.box.0badc0de", "../x.0badc0de", "no-suffix"} {
		_, err := c.RecordSign(bad, SignResult{Exit: 0, Summary: "1 signed"}, day)
		isRefusal(t, bad, err)
	}
	// A summary over several lines lands on one.
	if _, err := c.RecordSign(id, SignResult{Exit: 1, Summary: "the rebase stopped\nat a missed touch"}, day); err != nil {
		t.Error(err)
	}
	events, _ = p.Events()
	if got := events[len(events)-1].Summary; got != "the rebase stopped at a missed touch" {
		t.Errorf("summary %q", got)
	}
}

// An agent cannot log a sign-pass's events itself.
func TestSignKindsNotLoggable(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	for _, kind := range []string{"sign-requested", "signed", "sign-refused", "sign-failed", "sign-delivered"} {
		_, err := p.Log(Event{Kind: kind, Item: p.Name}, day)
		isRefusal(t, kind, err)
	}
}

// Each result reaches the session that asked for it once, as one line.
func TestDeliverSigns(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	id, _ := c.RequestSign("abc", "", []string{"loop"}, day)
	if got := c.DeliverSigns("abc", "", day); len(got) != 0 {
		t.Fatalf("delivered before the pass finished: %+v", got)
	}
	c.RecordSign(id, SignResult{Exit: 0, Summary: "1 signed; pushed loop"}, day)

	if got := c.DeliverSigns("other", "/elsewhere", day); len(got) != 0 {
		t.Errorf("another session got %+v", got)
	}
	got := c.DeliverSigns("abc", "", day.Add(time.Hour))
	if len(got) != 1 {
		t.Fatalf("delivered %+v", got)
	}
	if line := got[0].Line(); line != "sign-pass "+id+" signed: 1 signed; pushed loop" {
		t.Errorf("line %q", line)
	}
	if again := c.DeliverSigns("abc", "", day.Add(time.Hour)); len(again) != 0 {
		t.Errorf("delivered twice: %+v", again)
	}
	events, _ := p.Events()
	if last := events[len(events)-1]; last.Kind != "sign-delivered" || last.Sign != id || last.Delivers == "" {
		t.Errorf("logged %+v", last)
	}

	// A later result for the same ID, such as --resume after a failure, is
	// its own delivery.
	c.RecordSign(id, SignResult{Exit: 2, Summary: "no signing pass is in progress"}, day)
	got = c.DeliverSigns("abc", "/w/loop", day.Add(time.Hour))
	if len(got) != 1 || got[0].Line() != "sign-pass "+id+" refused: no signing pass is in progress" {
		t.Errorf("second result: %+v", got)
	}
}

// A result first seen more than a day after it finished is delivered as
// stale, for the record, and asks for nothing.
func TestDeliverStale(t *testing.T) {
	c := testConfig(t)
	newPlan(t, c, "x")
	id, _ := c.RequestSign("abc", "", nil, day)
	c.RecordSign(id, SignResult{Exit: 0, Summary: "1 signed"}, day)
	got := c.DeliverSigns("abc", "", day.Add(25*time.Hour))
	if len(got) != 1 || !got[0].Stale {
		t.Fatalf("delivered %+v", got)
	}
	line := got[0].Line()
	if !strings.Contains(line, "stale") || !strings.Contains(line, "no follow-up") || !strings.Contains(line, "1 signed") {
		t.Errorf("line %q", line)
	}
	id2, _ := c.RequestSign("abc", "", nil, day)
	c.RecordSign(id2, SignResult{Exit: 0, Summary: "1 signed"}, day)
	if got := c.DeliverSigns("abc", "", day.Add(23*time.Hour)); len(got) != 1 || got[0].Stale {
		t.Errorf("a result under a day old: %+v", got)
	}
}

// However many prompts arrive at once, a result is delivered once.
func TestDeliverSignsConcurrently(t *testing.T) {
	c := testConfig(t)
	newPlan(t, c, "x")
	id, _ := c.RequestSign("abc", "", nil, day)
	c.RecordSign(id, SignResult{Exit: 0, Summary: "1 signed"}, day)
	var wg sync.WaitGroup
	counts := make([]int, 16)
	for i := range counts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counts[i] = len(c.DeliverSigns("abc", "", day))
		}()
	}
	wg.Wait()
	total := 0
	for _, n := range counts {
		total += n
	}
	if total != 1 {
		t.Errorf("delivered %d times", total)
	}
}

// SignResults reads a result by its ID, and consuming it stops the next
// prompt delivering it again.
func TestSignResults(t *testing.T) {
	c := testConfig(t)
	newPlan(t, c, "x")
	id, _ := c.RequestSign("abc", "", nil, day)
	if got, err := c.SignResults(id, false, day); err != nil || len(got) != 0 {
		t.Fatalf("before the pass: %+v %v", got, err)
	}
	c.RecordSign(id, SignResult{Exit: 0, Summary: "2 signed"}, day)
	if got, _ := c.SignResults(id, false, day); len(got) != 1 {
		t.Fatalf("check: %+v", got)
	}
	got, err := c.SignResults(id, true, day)
	if err != nil || len(got) != 1 || got[0].Delivered || got[0].Line() != "sign-pass "+id+" signed: 2 signed" {
		t.Fatalf("consume: %+v %v", got, err)
	}
	if again := c.DeliverSigns("abc", "", day); len(again) != 0 {
		t.Errorf("remind delivered a consumed result: %+v", again)
	}
	if again, _ := c.SignResults(id, true, day); len(again) != 1 || !again[0].Delivered {
		t.Errorf("reading it again: %+v", again)
	}
	_, err = c.SignResults("2026-01-01-gone.box.0badc0de", false, day)
	isRefusal(t, "an unknown ID", err)
}

// A finished sign-pass reaches the session that handed it over on its next
// prompt, in each harness's form, and never a sub-agent's turn.
func TestRemindSignResult(t *testing.T) {
	event := func(fields map[string]any) []byte {
		data, _ := json.Marshal(fields)
		return data
	}
	waiter := "<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n" +
		"<summary>Background command \"until agent-plan sign-result X --check; do sleep 5; done\" completed (exit code 0)</summary>\n</task-notification>"
	for _, c2 := range []struct {
		name   string
		event  []byte
		format string // a hook event name, "eca", or "" for nothing
	}{
		{"Wesley's prompt in Claude Code", event(map[string]any{"session_id": "abc", "hook_event_name": "UserPromptSubmit",
			"prompt": "Carry on"}), "UserPromptSubmit"},
		{"the background waiter finishing in Claude Code", event(map[string]any{"session_id": "abc",
			"hook_event_name": "UserPromptSubmit", "prompt": waiter}), "UserPromptSubmit"},
		{"Wesley's prompt in Codex", event(map[string]any{"session_id": "abc", "turn_id": "t1",
			"hook_event_name": "UserPromptSubmit", "prompt": "Carry on"}), "UserPromptSubmit"},
		{"Wesley's prompt in ECA", event(map[string]any{"hook_type": "preRequest", "chat_id": "abc", "prompt": "Carry on"}), "eca"},
		{"a sub-agent's turn in Codex", event(map[string]any{"session_id": "abc", "agent_id": "a1",
			"hook_event_name": "UserPromptSubmit", "prompt": "go"}), ""},
		{"a sub-agent's chat in ECA", event(map[string]any{"hook_type": "preRequest", "chat_id": "abc",
			"parent_chat_id": "p", "prompt": "go"}), ""},
	} {
		t.Run(c2.name, func(t *testing.T) {
			c := testConfig(t)
			newPlan(t, c, "x")
			id, err := c.RequestSign("abc", "", []string{"loop"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			c.RecordSign(id, SignResult{Exit: 0, Summary: "1 signed; pushed loop"}, time.Now())
			out := c.Remind(c2.event)
			want := "sign-pass " + id + " signed: 1 signed; pushed loop"
			var got struct {
				Specific struct {
					Event   string `json:"hookEventName"`
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
				Context string `json:"additionalContext"`
			}
			json.Unmarshal(out, &got)
			switch c2.format {
			case "":
				if len(out) != 0 {
					t.Errorf("printed %s", out)
				}
				return
			case "eca":
				if got.Context != want {
					t.Errorf("want ECA's additionalContext %q, got %s", want, out)
				}
			default:
				if got.Specific.Event != c2.format || got.Specific.Context != want {
					t.Errorf("want %s's additionalContext %q, got %s", c2.format, want, out)
				}
			}
			if again := c.Remind(c2.event); len(again) != 0 {
				t.Errorf("delivered twice: %s", again)
			}
		})
	}
}
