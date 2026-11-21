package record

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A pull request's newest range record is the one reviewed last: settling
// a finding of an older one, which rewrites its UPDATED, leaves it older.
func TestNewestRange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	at := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	write := func(head string, reviewed, updated time.Time) string {
		base := strings.Repeat("0", 40)
		path, err := rangeFile(dir, base, head)
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
	rec, path, err := newestRange(dir, "o", "r", 7, "")
	if err != nil || path != second || rec.Head != strings.Repeat("2", 40) {
		t.Errorf("newest %v %q (%v), want %q", rec, path, err, second)
	}
	if rec, _, _ := newestRange(dir, "o", "r", 7, second); rec == nil || rec.Head != strings.Repeat("1", 40) {
		t.Errorf("newest but the second: %+v", rec)
	}
}

// Range records are kept under ranges/ by their commits' IDs alone; those
// an earlier agent-tools kept under OWNER/NAME/ are read where they are,
// and the one under ranges/ wins where both are.
func TestRangeLayouts(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	h1, h2, h3 := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40)
	// write puts a record of repo's pull request pr, reviewed at reviewed,
	// at path.
	write := func(t *testing.T, path, repo, head string, pr int, title string, reviewed time.Time) {
		t.Helper()
		if err := Update(path, func(r *Range) error {
			*r = Range{Repo: repo, Base: base, Head: head, PR: pr, Title: title, Updated: reviewed,
				Findings: []Finding{{ID: "F1", State: Open, Title: "x", Kind: "bug", Reviewer: "codex", Round: 1, Evidence: "e"}},
				Rounds:   []Round{{ID: "R1", Reviewer: "codex", Model: "m", At: reviewed, RedCheck: "absent"}}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// setUp gives a test's records directory, its ranges/ and its o/r/.
	setUp := func(t *testing.T) (dir, under, legacy string) {
		t.Helper()
		dir = t.TempDir()
		return dir, filepath.Join(dir, "ranges"), filepath.Join(dir, "o", "r")
	}

	t.Run("find", func(t *testing.T) {
		t.Parallel()
		dir, under, legacy := setUp(t)
		kept, old := filepath.Join(under, base+".."+h1+".org"), filepath.Join(legacy, base+".."+h1+".org")
		if got, err := findRange(dir, "o", "r", base, h1); err != nil || got != kept {
			t.Errorf("with no record, findRange = %q, %v; want %q", got, err, kept)
		}
		write(t, old, "o/r", h1, 0, "old", at)
		if got, err := findRange(dir, "o", "r", base, h1); err != nil || got != old {
			t.Errorf("with the old record alone, findRange = %q, %v; want %q", got, err, old)
		}
		if got, err := findRange(dir, "", "", base, h1); err != nil || got != kept {
			t.Errorf("with no GitHub name, findRange = %q, %v; want %q", got, err, kept)
		}
		write(t, kept, "o/r", h1, 0, "new", at)
		if got, err := findRange(dir, "o", "r", base, h1); err != nil || got != kept {
			t.Errorf("with both, findRange = %q, %v; want %q", got, err, kept)
		}
	})

	t.Run("an old record is written under ranges and left as it was", func(t *testing.T) {
		t.Parallel()
		dir, under, legacy := setUp(t)
		old := filepath.Join(legacy, base+".."+h1+".org")
		write(t, old, "o/r", h1, 0, "old", at)
		before, err := os.ReadFile(old)
		if err != nil {
			t.Fatal(err)
		}
		written, err := updateRange(dir, old, func(r *Range) error {
			r.Findings[0].State = Fix
			return nil
		})
		if want := filepath.Join(under, base+".."+h1+".org"); err != nil || written != want {
			t.Fatalf("updateRange = %q, %v; want %q", written, err, want)
		}
		rec, err := ReadRange(written)
		if err != nil || rec.Title != "old" || len(rec.Findings) != 1 || rec.Findings[0].State != Fix {
			t.Errorf("the written record %+v, %v", rec, err)
		}
		if after, err := os.ReadFile(old); err != nil || string(after) != string(before) {
			t.Errorf("the old record changed: %v\n%s", err, after)
		}
		// The next write starts from the record under ranges.
		if _, err := updateRange(dir, written, func(r *Range) error { r.Title = "again"; return nil }); err != nil {
			t.Fatal(err)
		}
		if rec, err := ReadRange(written); err != nil || rec.Findings[0].State != Fix || rec.Title != "again" {
			t.Errorf("the second write %+v, %v", rec, err)
		}
	})

	t.Run("ranges lists both layouts", func(t *testing.T) {
		t.Parallel()
		dir, under, legacy := setUp(t)
		write(t, filepath.Join(legacy, base+".."+h1+".org"), "o/r", h1, 0, "old 1", at)
		write(t, filepath.Join(legacy, base+".."+h2+".org"), "o/r", h2, 0, "old 2", at)
		write(t, filepath.Join(under, base+".."+h2+".org"), "o/r", h2, 0, "new 2", at)
		write(t, filepath.Join(under, base+".."+h3+".org"), "", h3, 0, "new 3", at)
		titles := func(listed []Listed) []string {
			var got []string
			for _, l := range listed {
				if l.Err != nil {
					t.Fatal(l.Err)
				}
				got = append(got, l.Range.Title)
			}
			return got
		}
		listed, err := ranges(dir, "o", "r", base)
		if got := strings.Join(titles(listed), ", "); err != nil || got != "old 1, new 2, new 3" {
			t.Errorf("ranges(o, r) = %s, %v; want old 1, new 2, new 3", got, err)
		}
		listed, err = ranges(dir, "", "", base)
		if got := strings.Join(titles(listed), ", "); err != nil || got != "new 2, new 3" {
			t.Errorf("ranges with no GitHub name = %s, %v; want new 2, new 3", got, err)
		}
	})

	t.Run("a pull request's newest across layouts, of its repository alone", func(t *testing.T) {
		t.Parallel()
		dir, under, legacy := setUp(t)
		write(t, filepath.Join(legacy, base+".."+h1+".org"), "o/r", h1, 7, "old", at)
		write(t, filepath.Join(under, base+".."+h2+".org"), "o/r", h2, 7, "new", at.Add(time.Hour))
		write(t, filepath.Join(under, base+".."+h3+".org"), "x/y", h3, 7, "another's", at.Add(2*time.Hour))
		// Another repository's pull request 7, whose record fails its
		// structure check, is passed over.
		drifted := strings.Repeat("4", 40)
		if err := os.WriteFile(filepath.Join(under, base+".."+drifted+".org"),
			[]byte("#+title: x\n* Review\n:PROPERTIES:\n:REPO: x/y\n:PR: 7\n:END:\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rec, _, err := newestRange(dir, "o", "r", 7, "")
		if err != nil || rec == nil || rec.Title != "new" {
			t.Errorf("newestRange = %+v, %v; want the record titled new", rec, err)
		}
		rec, path, err := newestRange(dir, "o", "r", 7, filepath.Join(under, base+".."+h2+".org"))
		if want := filepath.Join(legacy, base+".."+h1+".org"); err != nil || rec == nil || path != want {
			t.Errorf("newestRange but the new one = %+v, %q, %v; want %q", rec, path, err, want)
		}
	})

	t.Run("a range of a repository with no GitHub name", func(t *testing.T) {
		t.Parallel()
		_, under, _ := setUp(t)
		path := filepath.Join(under, base+".."+h1+".org")
		write(t, path, "", h1, 0, "local", at)
		if rec, err := ReadRange(path); err != nil || rec.Repo != "" || rec.Title != "local" {
			t.Errorf("ReadRange = %+v, %v", rec, err)
		}
	})
}
