package usage

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Wesley's answer holds until the window that prompted it resets.
func TestChoose(t *testing.T) {
	now := at.In(pdt)
	claude := func(t *testing.T) State {
		t.Helper()
		s := State{Dir: filepath.Join(t.TempDir(), "usage")}
		StatusLine(s, fixture(t, "claude-statusline.json"), now)
		return s
	}

	t.Run("until the window at 75% or more resets", func(t *testing.T) {
		s := claude(t)
		c, err := s.Choose("claude", "postpone", now)
		if err != nil {
			t.Fatal(err)
		}
		if c.Choice != "postpone" || c.Until != 1792032000 || c.DecidedAt != now.Unix() {
			t.Errorf("choice %+v", c)
		}
		if got, ok := s.Choice("claude", now); !ok || got != c {
			t.Errorf("got %+v, %v", got, ok)
		}
		if got, ok := s.Choice("claude", time.Unix(1792032000, 0)); ok {
			t.Errorf("still %+v after the reset", got)
		}
		if _, ok := s.Choice("codex", now); ok {
			t.Error("a choice for codex from claude's")
		}
	})

	t.Run("in the status line", func(t *testing.T) {
		s := claude(t)
		s.Choose("claude", "postpone", now)
		if got, want := StatusLine(s, []byte(`{"provider":"claude"}`), now), "5h 100%→19:40 · 7d 31% · wait→19:40"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if got, want := StatusLine(s, []byte(`{}`), now), "claude 5h 100%→19:40 · wait→19:40"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		s.Choose("claude", "keep", now)
		got := StatusLine(s, []byte(`{"provider":"claude"}`), now)
		if want := "5h 100%→19:40 · 7d 31%"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if strings.Contains(got, "↓") {
			t.Errorf("status line %q shows a tier", got)
		}
	})

	// The window at 75% or more that is used most binds; with none, the
	// one used most; with no reading, five hours from now.
	t.Run("which window", func(t *testing.T) {
		s := State{Dir: filepath.Join(t.TempDir(), "usage")}
		s.Record(Reading{Provider: "codex", ObservedAt: now.Unix(), Windows: []Window{
			{Name: "5h", Used: 80, ResetsAt: 1792024111}, {Name: "7d", Used: 91, ResetsAt: 1792567353}}})
		if c, _ := s.Choose("codex", "postpone", now); c.Until != 1792567353 {
			t.Errorf("until %d", c.Until)
		}
		s.Record(Reading{Provider: "codex", ObservedAt: now.Unix(), Windows: []Window{
			{Name: "5h", Used: 30, ResetsAt: 1792024111}, {Name: "7d", Used: 2, ResetsAt: 1792567353}}})
		if c, _ := s.Choose("codex", "keep", now); c.Until != 1792024111 {
			t.Errorf("until %d", c.Until)
		}
		if c, _ := s.Choose("claude", "keep", now); c.Until != now.Add(5*time.Hour).Unix() {
			t.Errorf("until %d", c.Until)
		}
	})

	t.Run("refused", func(t *testing.T) {
		s := claude(t)
		for _, args := range [][2]string{{"claude", "downgrade"}, {"gemini", "keep"}, {"../x", "keep"}} {
			if _, err := s.Choose(args[0], args[1], now); err == nil {
				t.Errorf("chose %v", args)
			}
		}
		_, err := s.Choose("claude", "down", now)
		if err == nil || !strings.Contains(err.Error(), "keep or postpone") {
			t.Errorf("down: %v", err)
		}
		if _, ok := s.Choice("claude", now); ok {
			t.Error("down was recorded")
		}
	})
}

// The question and answer a plan records for the choice.
func TestQuestion(t *testing.T) {
	now := at.In(pdt)
	r, _ := FromClaudeStatus(fixture(t, "claude-statusline.json"), now)
	q := Question(r, true, now)
	if q.Text != "Claude's 5-hour window is at 100%, resetting 19:40: keep going, or postpone the task until then?" {
		t.Errorf("question %q", q.Text)
	}
	if q.Recommendation != "Keep going" || len(q.Alternatives) != 1 || q.Alternatives[0] != "Postpone the task until 19:40" {
		t.Errorf("question %+v", q)
	}
	if len(q.Answers) != 2 || q.Answers["keep"] != "Keep going" || q.Answers["postpone"] != "Postpone the task until 19:40" {
		t.Errorf("answers %v", q.Answers)
	}
	if q := Question(Reading{Provider: "codex"}, false, now); !strings.HasPrefix(q.Text, "Codex's quota is unknown on this host") {
		t.Errorf("question without a reading %q", q.Text)
	}
}

// What `agent-plan usage` prints.
func TestSummary(t *testing.T) {
	now := at.In(pdt)
	s := State{Dir: filepath.Join(t.TempDir(), "usage")}
	StatusLine(s, fixture(t, "claude-statusline.json"), now.Add(-2*time.Minute))
	s.Choose("claude", "postpone", now)
	got := SummaryText(s.Summary(now), now)
	want := "claude: 5h 100% resets 19:40, 7d 31% resets Wed 21 10:00 (seen 16:38); choice postpone until 19:40\n" +
		"codex: unknown\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
