package usage

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// at is a time between the fixtures' observation and their earliest reset.
var at = time.Unix(1792021200, 0)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Claude Code's status line input carries the account's windows once the
// session has had a response.
func TestFromClaudeStatus(t *testing.T) {
	t.Run("before the first response", func(t *testing.T) {
		if r, ok := FromClaudeStatus(fixture(t, "claude-statusline-first.json"), at); ok {
			t.Errorf("reading %+v from an input without rate_limits", r)
		}
	})
	t.Run("after it", func(t *testing.T) {
		r, ok := FromClaudeStatus(fixture(t, "claude-statusline.json"), at)
		want := Reading{Provider: "claude", ObservedAt: at.Unix(), Windows: []Window{
			{Name: "5h", Used: 100, ResetsAt: 1792032000},
			{Name: "7d", Used: 31, ResetsAt: 1792602000},
		}}
		if !ok || !reflect.DeepEqual(r, want) {
			t.Errorf("got %+v, %v; want %+v", r, ok, want)
		}
	})
	t.Run("not JSON", func(t *testing.T) {
		if _, ok := FromClaudeStatus([]byte("{"), at); ok {
			t.Error("a reading from broken input")
		}
	})
}

// Codex's rollout records the account's windows on each token_count.
func TestFromCodex(t *testing.T) {
	want := Reading{Provider: "codex", ObservedAt: at.Unix(), Windows: []Window{
		{Name: "5h", Used: 11, ResetsAt: 1792024111},
		{Name: "7d", Used: 2, ResetsAt: 1792567353},
	}}
	line := fixture(t, "codex-token-count.jsonl")

	t.Run("a token_count line", func(t *testing.T) {
		r, ok := FromCodexTokenCount(line, at)
		if !ok || !reflect.DeepEqual(r, want) {
			t.Errorf("got %+v, %v; want %+v", r, ok, want)
		}
	})

	t.Run("the rollout's last token_count", func(t *testing.T) {
		older := strings.Replace(string(line), `"used_percent":11.0`, `"used_percent":5.0`, 1)
		other := `{"timestamp":"2026-10-14T23:37:38.000Z","type":"response_item","payload":{"type":"message"}}` + "\n"
		// A long line before them all, as a big tool output makes, is
		// passed over.
		filler := `{"type":"response_item","payload":{"output":"` + strings.Repeat("x", 3<<20) + `"}}` + "\n"
		path := filepath.Join(t.TempDir(), "rollout.jsonl")
		if err := os.WriteFile(path, []byte(filler+older+string(line)+other), 0o644); err != nil {
			t.Fatal(err)
		}
		r, ok := FromCodexRollout(path, at)
		if !ok || !reflect.DeepEqual(r, want) {
			t.Errorf("got %+v, %v; want %+v", r, ok, want)
		}
	})

	t.Run("a rollout without one", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "rollout.jsonl")
		os.WriteFile(path, []byte(`{"type":"session_meta","payload":{}}`+"\n"), 0o644)
		if r, ok := FromCodexRollout(path, at); ok {
			t.Errorf("reading %+v", r)
		}
		if r, ok := FromCodexRollout(filepath.Join(t.TempDir(), "missing.jsonl"), at); ok {
			t.Errorf("reading %+v from a missing file", r)
		}
	})
}

// A reading is kept per host and provider, and a window reads as unknown
// once it has reset.
func TestState(t *testing.T) {
	s := State{Dir: filepath.Join(t.TempDir(), "usage")}
	if r, ok := s.Reading("claude", at); ok {
		t.Fatalf("reading %+v before any was recorded", r)
	}
	r, _ := FromClaudeStatus(fixture(t, "claude-statusline.json"), at)
	if err := s.Record(r); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Reading("claude", at); !ok || !reflect.DeepEqual(got, r) {
		t.Errorf("got %+v, %v; want %+v", got, ok, r)
	}
	if got, ok := s.Reading("codex", at); ok {
		t.Errorf("codex reading %+v from claude's", got)
	}

	t.Run("after the 5-hour reset", func(t *testing.T) {
		got, ok := s.Reading("claude", time.Unix(1792032000, 0))
		if !ok || len(got.Windows) != 1 || got.Windows[0].Name != "7d" {
			t.Errorf("got %+v, %v", got, ok)
		}
	})
	t.Run("after both", func(t *testing.T) {
		if got, ok := s.Reading("claude", time.Unix(1792602000, 0)); ok {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("replaced whole", func(t *testing.T) {
		newer := Reading{Provider: "claude", ObservedAt: at.Unix() + 60, Windows: []Window{{Name: "5h", Used: 3, ResetsAt: 1792050000}}}
		if err := s.Record(newer); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Reading("claude", at); !reflect.DeepEqual(got, newer) {
			t.Errorf("got %+v", got)
		}
		entries, _ := os.ReadDir(s.Dir)
		for _, e := range entries {
			if e.Name() != "claude.json" {
				t.Errorf("left %s behind", e.Name())
			}
		}
	})

	t.Run("an unknown provider", func(t *testing.T) {
		if err := s.Record(Reading{Provider: "../x"}); err == nil {
			t.Error("recorded a reading for a provider named as a path")
		}
	})
}
