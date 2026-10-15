package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A main thread's prompt asks about tiers once per window and threshold on
// the host, whichever session sends it.
func TestRemind(t *testing.T) {
	now := at.In(pdt)
	prompt := fixture(t, "claude-user-prompt.json")
	state := func(t *testing.T, used float64, resets int64) State {
		t.Helper()
		s := State{Dir: filepath.Join(t.TempDir(), "usage")}
		if used >= 0 {
			s.Record(Reading{Provider: "claude", ObservedAt: now.Unix(), Windows: []Window{
				{Name: "5h", Used: used, ResetsAt: resets}, {Name: "7d", Used: 31, ResetsAt: 1792602000}}})
		}
		return s
	}

	t.Run("Claude Code, at 100%", func(t *testing.T) {
		s := state(t, 100, 1792032000)
		ask := Remind(s, prompt, now)
		for _, want := range []string{"Claude's 5-hour window is at 100%, resetting 19:40", "Ask Wesley once",
			"one tier down (Opus to Sonnet, Sonnet to Haiku)", "reviewers keep theirs",
			"Change no running agent's or session's model", "Until he answers, keep the usual tiers",
			"`agent-plan usage choose claude keep|down|postpone --plan PLAN`",
			"`agent-plan postpone PLAN ID --until '2026-10-14 19:40'`"} {
			if !strings.Contains(ask, want) {
				t.Errorf("ask lacks %q:\n%s", want, ask)
			}
		}
		// Another session on the host is not asked again.
		if again := Remind(State{Dir: s.Dir}, prompt, now); again != "" {
			t.Errorf("asked again: %s", again)
		}
	})

	t.Run("the thresholds", func(t *testing.T) {
		s := state(t, 74, 1792032000)
		if ask := Remind(s, prompt, now); ask != "" {
			t.Errorf("asked at 74%%: %s", ask)
		}
		for _, c := range []struct {
			used float64
			ask  bool
		}{{75, true}, {89, false}, {90, true}, {100, false}} {
			s.Record(Reading{Provider: "claude", ObservedAt: now.Unix(), Windows: []Window{{Name: "5h", Used: c.used, ResetsAt: 1792032000}}})
			if ask := Remind(s, prompt, now); (ask != "") != c.ask {
				t.Errorf("at %v%%: asked %q, want an ask: %v", c.used, ask, c.ask)
			}
		}
		// The next window is a new one.
		later := time.Unix(1792032000, 0).In(pdt)
		s.Record(Reading{Provider: "claude", ObservedAt: later.Unix(), Windows: []Window{{Name: "5h", Used: 76, ResetsAt: 1792050000}}})
		if ask := Remind(s, prompt, later); ask == "" {
			t.Error("the next window's 75% did not ask")
		}
		// Marks for windows that have reset are cleared.
		entries, _ := os.ReadDir(filepath.Join(s.Dir, "asked"))
		if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "claude-5h-1792050000-") {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("marks %v", names)
		}
	})

	t.Run("with a choice", func(t *testing.T) {
		s := state(t, 80, 1792032000)
		s.Choose("claude", "down", now)
		s.Record(Reading{Provider: "claude", ObservedAt: now.Unix(), Windows: []Window{{Name: "5h", Used: 92, ResetsAt: 1792032000}}})
		if ask := Remind(s, prompt, now); !strings.Contains(ask, "His answer so far: down until 19:40.") {
			t.Errorf("ask %q", ask)
		}
	})

	t.Run("nothing known", func(t *testing.T) {
		if ask := Remind(state(t, -1, 0), prompt, now); ask != "" {
			t.Errorf("ask %q", ask)
		}
	})

	t.Run("Codex, from its rollout", func(t *testing.T) {
		dir := t.TempDir()
		rollout := filepath.Join(dir, "rollout.jsonl")
		line := strings.Replace(string(fixture(t, "codex-token-count.jsonl")), `"used_percent":11.0`, `"used_percent":91.0`, 1)
		os.WriteFile(rollout, []byte(line), 0o644)
		input := func(agent string) []byte {
			data, _ := json.Marshal(map[string]any{"session_id": "s", "transcript_path": rollout, "cwd": dir, "model": "gpt-5.5",
				"permission_mode": "default", "hook_event_name": "UserPromptSubmit", "prompt": "go on", "turn_id": "t1",
				"agent_id": agent})
			return data
		}
		s := State{Dir: filepath.Join(dir, "usage")}
		if ask := Remind(s, input("a1"), now); ask != "" {
			t.Errorf("a sub-agent's turn asked: %s", ask)
		}
		ask := Remind(s, input(""), now)
		if !strings.Contains(ask, "Codex's 5-hour window is at 91%, resetting 17:28") ||
			!strings.Contains(ask, "`agent-plan usage choose codex") {
			t.Errorf("ask %q", ask)
		}
		if r, ok := s.Reading("codex", now); !ok || r.Windows[0].Used != 91 {
			t.Errorf("reading %+v, %v", r, ok)
		}
	})

	t.Run("ECA, by its model's provider", func(t *testing.T) {
		input := func(model, parent string) []byte {
			data, _ := json.Marshal(map[string]any{"hook_type": "preRequest", "chat_id": "c", "agent": "code",
				"full_model": model, "prompt": "go on", "parent_chat_id": parent})
			return data
		}
		s := state(t, 95, 1792032000)
		if ask := Remind(s, input("anthropic/claude-opus-5-5", "p"), now); ask != "" {
			t.Errorf("a sub-agent's chat asked: %s", ask)
		}
		if ask := Remind(s, input("ollama/qwen", ""), now); ask != "" {
			t.Errorf("a local model asked: %s", ask)
		}
		if ask := Remind(s, input("anthropic/claude-opus-5-5", ""), now); !strings.Contains(ask, "Claude's 5-hour window") {
			t.Errorf("ask %q", ask)
		}
	})

	t.Run("other events", func(t *testing.T) {
		s := state(t, 100, 1792032000)
		for _, input := range []string{`{"hook_event_name":"Stop","session_id":"s"}`, `{"hook_type":"postRequest"}`, `{`} {
			if ask := Remind(s, []byte(input), now); ask != "" {
				t.Errorf("%s asked: %s", input, ask)
			}
		}
	})
}
