package record

import (
	"strings"
	"testing"
	"time"
)

// A pull request's newest range record is the one reviewed last: settling
// a finding of an older one, which rewrites its UPDATED, leaves it older.
func TestNewestRange(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	at := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	write := func(head string, reviewed, updated time.Time) string {
		base := strings.Repeat("0", 40)
		path, err := RangeFile("o", "r", base, head)
		if err != nil {
			t.Fatal(err)
		}
		if err := Update(path, func(r *Range) error {
			*r = Range{Repo: "o/r", Base: base, Head: head, PR: 7, Title: "t", Updated: updated,
				Rounds: []Round{{ID: "R1", Reviewer: "codex", Model: "m", At: reviewed, RedCheck: "absent"}}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write(strings.Repeat("1", 40), at, at.Add(2*time.Hour))
	second := write(strings.Repeat("2", 40), at.Add(time.Hour), at.Add(time.Hour))
	rec, path, err := NewestRange("o", "r", 7, "")
	if err != nil || path != second || rec.Head != strings.Repeat("2", 40) {
		t.Errorf("newest %v %q (%v), want %q", rec, path, err, second)
	}
	if rec, _, _ := NewestRange("o", "r", 7, second); rec == nil || rec.Head != strings.Repeat("1", 40) {
		t.Errorf("newest but the second: %+v", rec)
	}
}
