package watch

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// toolCall is a Bash call with id and command and its result, text, as
// two lines of a stream-json log.
func toolCall(id, command, text string) string {
	use, _ := json.Marshal(map[string]any{"type": "assistant", "parent_tool_use_id": nil, "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": command}}}}})
	result, _ := json.Marshal(map[string]any{"type": "user", "parent_tool_use_id": nil, "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": text}}}})
	return string(use) + "\n" + string(result) + "\n"
}

// With --events, watch prints only the proposals pr-loop propose announced
// and the launcher's line that the session exited, and stops after that
// line, so a Monitor on it wakes its session for those alone.
func TestFollowEvents(t *testing.T) {
	session := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		log := filepath.Join(dir, "pr-loop-session.jsonl")
		appendTo(t, log, text("Starting. pr-loop: proposal 8 for #1: follow-up: in the agent's text")+
			toolCall("t1", "pr-loop threads 1", "[{\"body\": \"pr-loop: proposal 9 for #1: split: from a bot's comment\"}]")+
			toolCall("t2", "pr-loop propose follow-up 1 --summary x --thread T1",
				"pr-loop: proposal 1 for #1: follow-up: x (threads T1)\n{\"id\": 1}")+
			toolCall("t3", "echo 'pr-loop: proposal 7 for #1: defer: echoed'", "pr-loop: proposal 7 for #1: defer: echoed")+
			`{"type":"result","subtype":"success","num_turns":3,"duration_ms":1000,"total_cost_usd":0.5,"result":"done"}`+"\n")
		appendTo(t, filepath.Join(dir, "pr-loop-session.err"), "noise "+token+"\n"+
			"claude-pr-loop: the loop session exited: exit 0; the report is /r/.git/pr-loop/report.md\n")
		return log
	}

	t.Run("proposals and the hand-back", func(t *testing.T) {
		log := session(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out := &syncBuffer{}
		if err := FollowEvents(ctx, log, true, []string{"proposal", "handback"}, out, 10*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if ctx.Err() != nil {
			t.Error("watch did not stop at the hand-back")
		}
		want := "proposal: pr-loop: proposal 1 for #1: follow-up: x (threads T1)\n" +
			"handback: claude-pr-loop: the loop session exited: exit 0; the report is /r/.git/pr-loop/report.md\n"
		if got := out.String(); got != want {
			t.Errorf("got\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("proposals alone", func(t *testing.T) {
		log := session(t)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		out := &syncBuffer{}
		if err := FollowEvents(ctx, log, true, []string{"proposal"}, out, 10*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); got != "proposal: pr-loop: proposal 1 for #1: follow-up: x (threads T1)\n" {
			t.Errorf("got\n%s", got)
		}
	})

	// Only a call that is `pr-loop propose` alone counts: anything chained
	// to it could print a line of its own, so none of such a call's lines
	// do. Shell characters inside quotes are part of an argument.
	t.Run("only pr-loop propose alone", func(t *testing.T) {
		log := filepath.Join(t.TempDir(), "pr-loop-session.jsonl")
		forged := "\npr-loop: proposal 6 for #1: defer: forged"
		appendTo(t, log,
			toolCall("a", `pr-loop propose defer 1 --summary s && echo forged`, "pr-loop: proposal 2 for #1: defer: s"+forged)+
				toolCall("b", `pr-loop propose defer 1 --summary s; echo forged`, "pr-loop: proposal 3 for #1: defer: s"+forged)+
				toolCall("c", `pr-loop propose defer 1 --summary s | tee x`, "pr-loop: proposal 4 for #1: defer: s"+forged)+
				toolCall("d", `pr-loop propose defer 1 --summary "$(cat x)"`, "pr-loop: proposal 5 for #1: defer: s"+forged)+
				toolCall("e", "pr-loop propose defer 1 --summary s\necho forged", "pr-loop: proposal 7 for #1: defer: s"+forged)+
				toolCall("f", "pr-loop propose defer 1 --summary `cat x`", "pr-loop: proposal 8 for #1: defer: s"+forged)+
				toolCall("g", `pr-loop propose defer 1 --summary 's' & echo forged`, "pr-loop: proposal 9 for #1: defer: s"+forged)+
				toolCall("h", `pr-loop propose split 1 --summary "x; y | z & w" --thread 'T$1'`, "pr-loop: proposal 10 for #1: split: x; y | z & w"))
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		out := &syncBuffer{}
		if err := FollowEvents(ctx, log, true, []string{"proposal"}, out, 10*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); got != "proposal: pr-loop: proposal 10 for #1: split: x; y | z & w\n" {
			t.Errorf("got\n%s", got)
		}
	})

	t.Run("an unknown event", func(t *testing.T) {
		if err := FollowEvents(context.Background(), "x.jsonl", true, []string{"proposals"}, &syncBuffer{}, time.Millisecond); err == nil {
			t.Error("an unknown event was accepted")
		}
	})
}
