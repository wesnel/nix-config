package dashboard

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/export"
	"github.com/wesnel/agent-tools/internal/record"
)

// GitHub is read every five minutes: a change in the pull requests the
// page lists prints LineReviews, but one only in what their records hold
// does not; a review record of a listed pull request appearing or
// changing prints LineRecord at the next look, and one of another pull
// request nothing.
func TestWatchReviews(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c, _, p := watchFixture(t)
	s := newScript(t, c, p)
	gh := export.GitHub{At: "2026-10-17T10:00:00Z", Mine: []export.PR{{Repo: "o/r", Number: 7, Title: "Add the loop", CI: "none"}}}
	reads := 0
	s.w.GitHub = func(time.Time) (export.GitHub, error) {
		reads++
		g := gh
		g.Mine = append([]export.PR(nil), gh.Mine...)
		return g, nil
	}
	base := strings.Repeat("0", 40)
	// writeRecord writes a record of owner/repo's pull request pr under
	// ranges/, or, where legacy is set, under owner/repo, where records
	// were kept before.
	writeRecord := func(legacy bool, owner, repo string, pr int, head, title string) func() {
		return func() {
			path, err := record.RangeFile(base, head)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				path = filepath.Join(record.Dir(), owner, repo, filepath.Base(path))
			}
			if err := record.Update(path, func(r *record.Range) error {
				*r = record.Range{Repo: owner + "/" + repo, Base: base, Head: head, PR: pr, Title: title, Updated: now}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	steps := make([]func(), 16)
	for i := range steps {
		steps[i] = nothing
	}
	steps[0] = func() { gh.Mine[0].RecordThreads = 3 } // read at poll 10, records only: no line
	h1, h2, h3 := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40)
	steps[11] = writeRecord(false, "o", "r", 7, h1, "Add the loop")       // a record appears
	steps[12] = writeRecord(false, "o", "r", 7, h1, "Add the loop again") // and changes
	steps[13] = writeRecord(false, "p", "q", 3, h2, "Not listed")         // another's
	steps[14] = writeRecord(true, "o", "r", 7, h3, "Kept under o/r")      // one where records were kept before
	got := s.run(t, steps...)
	want := map[int]string{10: "", 12: LineRecord + "\n", 13: LineRecord + "\n", 15: LineRecord + "\n"}
	for i, line := range got {
		if w := want[i]; line != w {
			t.Errorf("poll %d printed %q, want %q", i, line, w)
		}
	}
	if reads != 2 {
		t.Errorf("GitHub read %d times over %d polls, want at 0 and 5 minutes", reads, len(got))
	}

	// A change in CI, after a restart, is printed at the next read, five
	// minutes after the last.
	more := make([]func(), 10)
	for i := range more {
		more[i] = nothing
	}
	more[0] = func() { gh.Mine[0].CI = "success" }
	got = s.run(t, more...)
	if strings.Count(strings.Join(got, ""), LineReviews) != 1 || reads != 3 {
		t.Errorf("printed %q after %d reads, want one %q", got, reads, LineReviews)
	}
}

// A GitHub read that fails waits the five minutes too, and prints nothing.
func TestWatchReviewsFailing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c, _, p := watchFixture(t)
	s := newScript(t, c, p)
	reads := 0
	s.w.GitHub = func(time.Time) (export.GitHub, error) {
		reads++
		return export.GitHub{}, errors.New("GitHub: rate limited")
	}
	steps := make([]func(), 20)
	for i := range steps {
		steps[i] = nothing
	}
	got := s.run(t, steps...)
	if reads < 2 || reads > 3 {
		t.Errorf("GitHub read %d times over %d polls while failing, want every 5 minutes", reads, len(got))
	}
	if strings.Join(got, "") != "" {
		t.Errorf("printed %q while failing", got)
	}
}
