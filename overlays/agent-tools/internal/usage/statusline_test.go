package usage

import (
	"path/filepath"
	"testing"
	"time"
)

// pdt is the zone the reset times are shown in: the operator's.
var pdt = time.FixedZone("PDT", -7*3600)

// The status line shows the session's context and its provider's
// windows, with the reset of a window at or over 75%.
func TestStatusLine(t *testing.T) {
	now := at.In(pdt)
	state := func(t *testing.T) State {
		t.Helper()
		return State{Dir: filepath.Join(t.TempDir(), "usage")}
	}
	codex := func(t *testing.T, s State) {
		t.Helper()
		r, _ := FromCodexTokenCount(fixture(t, "codex-token-count.jsonl"), now)
		if err := s.Record(r); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("Claude Code, which records its reading", func(t *testing.T) {
		s := state(t)
		if got, want := StatusLine(s, fixture(t, "claude-statusline.json"), now), "ctx 4% · 5h 100%→19:40 · 7d 31%"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if _, ok := s.Reading("claude", now); !ok {
			t.Error("the reading was not recorded")
		}
		// A later session's first input has neither context nor quota
		// of its own, so it shows the recorded windows.
		if got, want := StatusLine(s, fixture(t, "claude-statusline-first.json"), now), "5h 100%→19:40 · 7d 31%"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Claude Code before anything is known", func(t *testing.T) {
		if got := StatusLine(state(t), fixture(t, "claude-statusline-first.json"), now); got != "" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Emacs, for a session of a provider", func(t *testing.T) {
		s := state(t)
		codex(t, s)
		input := `{"cwd":"/w","provider":"codex","context_window":{"used_percentage":42.7}}`
		if got, want := StatusLine(s, []byte(input), now), "ctx 42% · 5h 11% · 7d 2%"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		// A provider with no reading on this host shows context alone.
		input = `{"cwd":"/w","provider":"claude","context_window":{"used_percentage":7}}`
		if got, want := StatusLine(s, []byte(input), now), "ctx 7%"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Emacs, outside a session", func(t *testing.T) {
		s := state(t)
		codex(t, s)
		if got := StatusLine(s, []byte(`{"cwd":"/w"}`), now); got != "" {
			t.Errorf("a provider under 75%% shown: %q", got)
		}
		StatusLine(s, fixture(t, "claude-statusline.json"), now)
		if got, want := StatusLine(s, []byte(`{"cwd":"/w"}`), now), "claude 5h 100%→19:40"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("a reset more than a day away", func(t *testing.T) {
		s := state(t)
		s.Record(Reading{Provider: "claude", ObservedAt: now.Unix(), Windows: []Window{{Name: "7d", Used: 80, ResetsAt: 1792602000}}})
		if got, want := StatusLine(s, []byte(`{"provider":"claude"}`), now), "7d 80%→Wed 21 10:00"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("broken input", func(t *testing.T) {
		if got := StatusLine(state(t), []byte("{"), now); got != "" {
			t.Errorf("got %q", got)
		}
	})
}
