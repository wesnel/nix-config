package usage

import (
	"testing"
	"time"
)

// pdt is the zone the reset times are shown in: Wesley's.
var pdt = time.FixedZone("PDT", -7*3600)

// The status line shows the session's context and the account's windows,
// with the reset of a window at or over 75%.
func TestStatusLine(t *testing.T) {
	now := at.In(pdt)

	t.Run("Claude Code", func(t *testing.T) {
		if got, want := StatusLine(fixture(t, "claude-statusline.json"), now), "ctx 4% · 5h 100%→19:40 · 7d 31%"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Claude Code before anything is known", func(t *testing.T) {
		if got := StatusLine(fixture(t, "claude-statusline-first.json"), now); got != "" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("windows under 75%", func(t *testing.T) {
		input := `{"context_window":{"used_percentage":42.7},"rate_limits":{` +
			`"five_hour":{"used_percentage":11,"resets_at":1792024111},` +
			`"seven_day":{"used_percentage":2,"resets_at":1792567353}}}`
		if got, want := StatusLine([]byte(input), now), "ctx 42% · 5h 11% · 7d 2%"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("a reset more than a day away", func(t *testing.T) {
		input := `{"rate_limits":{"seven_day":{"used_percentage":80,"resets_at":1792602000}}}`
		if got, want := StatusLine([]byte(input), now), "7d 80%→Wed 21 10:00"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("a window that has reset", func(t *testing.T) {
		later := time.Unix(1792032000, 0).In(pdt)
		if got, want := StatusLine(fixture(t, "claude-statusline.json"), later), "ctx 4% · 7d 31%"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("broken input", func(t *testing.T) {
		if got := StatusLine([]byte("{"), now); got != "" {
			t.Errorf("got %q", got)
		}
	})
}
