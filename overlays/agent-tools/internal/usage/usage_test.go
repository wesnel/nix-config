package usage

import (
	"os"
	"path/filepath"
	"reflect"
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
