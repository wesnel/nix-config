package plans

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	gosync "sync"
	"testing"
	"time"
)

// notesConfig is a config with a notes repository of the test's own, which
// any host writes the notes files in.
func notesConfig(t *testing.T) (c Config, repo string) {
	t.Helper()
	c = testConfig(t)
	return c, filepath.Dir(c.Dir)
}

// friday is a Friday, the day a digest is named for.
var friday = time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)

// demo is a digest in the format the export parses: a level-1 heading per
// change, with its plan and pull request.
const demo = `#+title: Demo 2026-11-06
The week's changes.
* Add the run loop
:PROPERTIES:
:PLAN: [[id:867f8a68-d069-4a03-9d20-a2c14ae0c2d5][Run loop]]
:PR: wesnel/nix-config#243
:END:
Start the loop.
#+begin_src sh
pr-loop status 243
#+end_src
** A detail
* Draw the digest
:PROPERTIES:
:PR: https://github.com/wesnel/nix-config/pull/244
:PLAN: id:867f8a68-d069-4a03-9d20-a2c14ae0c2d5
:END:
`

// refusedSaying checks that err is a refusal that says what to do instead
// and names want.
func refusedSaying(t *testing.T, err error, want string) {
	t.Helper()
	isRefusal(t, want, err)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("got %v, want a refusal saying %q", err, want)
	}
}

func TestWriteDigest(t *testing.T) {
	t.Run("writes and rewrites", func(t *testing.T) {
		c, repo := notesConfig(t)
		file := filepath.Join(repo, "digests", "2026", "11-06-demo.org")
		// The sync's watcher sees the plans directory's entries change.
		old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		touched := func() bool {
			info, err := os.Stat(c.Dir)
			return err == nil && info.ModTime().After(old)
		}
		os.MkdirAll(c.Dir, 0o755)
		os.Chtimes(c.Dir, old, old)
		got, err := c.WriteDigest(friday, demo)
		if err != nil || got.File != file || !got.Changed {
			t.Fatalf("WriteDigest: %+v, %v", got, err)
		}
		if data, _ := os.ReadFile(file); string(data) != demo {
			t.Errorf("the digest holds\n%s", data)
		}
		before, _ := os.Stat(file)
		os.Chtimes(c.Dir, old, old)
		if got, err := c.WriteDigest(friday, demo); err != nil || got.Changed {
			t.Errorf("the same digest again: %+v, %v", got, err)
		}
		if after, _ := os.Stat(file); !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) || touched() {
			t.Errorf("the same digest again replaced the file, or touched the plans directory")
		}
		newer := demo + "More.\n"
		if got, err := c.WriteDigest(friday, newer); err != nil || !got.Changed || !touched() {
			t.Errorf("a newer digest: %+v, %v; the plans directory touched: %v", got, err, touched())
		}
		if data, _ := os.ReadFile(file); string(data) != newer {
			t.Errorf("the newer digest holds\n%s", data)
		}
	})
	t.Run("as large as the export reads", func(t *testing.T) {
		c, _ := notesConfig(t)
		text := demo + strings.Repeat("x", 64<<10-len(demo)-1) + "\n"
		if got, err := c.WriteDigest(friday, text); err != nil || !got.Changed {
			t.Errorf("a digest of 64 KiB: %+v, %v", got, err)
		}
	})
	// A block's lines are its text: a star line in a demo step's output
	// is no change.
	t.Run("a star line in a block", func(t *testing.T) {
		c, _ := notesConfig(t)
		text := demo + "#+begin_example\n* not a change\n#+end_example\n"
		if got, err := c.WriteDigest(friday, text); err != nil || !got.Changed {
			t.Errorf("a digest whose output holds a star line: %+v, %v", got, err)
		}
	})
	refused := map[string]struct {
		day        time.Time
		text, want string
	}{
		"not a Friday": {day: friday.AddDate(0, 0, -1), text: demo, want: "Thursday"},
		"over 64 KiB":  {text: demo + strings.Repeat("x", 64<<10-len(demo)) + "\n", want: "64 KiB"},
		"no change":    {text: "Just text.\n", want: "level-1 heading"},
		"changes only in a block": {text: "#+begin_example\n* Loop\n:PROPERTIES:\n:PR: o/r#1\n:PLAN: id:abc-1\n:END:\n#+end_example\n",
			want: "level-1 heading"},
		"a change without PR":       {text: "* Loop\n:PROPERTIES:\n:PLAN: id:abc-1\n:END:\n", want: "PR"},
		"a change without PLAN":     {text: "* Loop\n:PROPERTIES:\n:PR: o/r#1\n:END:\n", want: "PLAN"},
		"a change without a drawer": {text: "* Loop\n:PR: o/r#1\n", want: "property drawer"},
		"a drawer left open":        {text: "* Loop\n:PROPERTIES:\n:PR: o/r#1\n:PLAN: id:abc-1\n", want: ":END:"},
		"a PR of no form":           {text: "* Loop\n:PROPERTIES:\n:PR: 243\n:PLAN: id:abc-1\n:END:\n", want: "PR"},
		"a PLAN not an id link":     {text: "* Loop\n:PROPERTIES:\n:PR: o/r#1\n:PLAN: the loop\n:END:\n", want: "PLAN"},
		"a NUL":                     {text: demo + "\x00\n", want: "NUL"},
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			c, repo := notesConfig(t)
			day := tc.day
			if day.IsZero() {
				day = friday
			}
			got, err := c.WriteDigest(day, tc.text)
			refusedSaying(t, err, tc.want)
			if _, err := os.Lstat(filepath.Join(repo, "digests")); err == nil || got.Changed {
				t.Errorf("a refused digest wrote %+v", got)
			}
		})
	}
	t.Run("through a linked directory", func(t *testing.T) {
		c, repo := notesConfig(t)
		elsewhere := t.TempDir()
		os.MkdirAll(repo, 0o755)
		os.Symlink(elsewhere, filepath.Join(repo, "digests"))
		_, err := c.WriteDigest(friday, demo)
		refusedSaying(t, err, "link")
		if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
			t.Errorf("wrote through the link: %v", entries)
		}
	})
}

// planID is the file-level ID of the plan p, as its file holds it.
func planID(t *testing.T, p Plan) string {
	t.Helper()
	data, _ := os.ReadFile(p.File())
	m := regexp.MustCompile(`(?m)^:ID: (\S+)$`).FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("%s has no ID", p.File())
	}
	return m[1]
}

func TestWriteBrag(t *testing.T) {
	merged := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	t.Run("one entry per pull request", func(t *testing.T) {
		c, repo := notesConfig(t)
		p := newPlan(t, c, "loop")
		id := planID(t, p)
		o := BragOptions{Day: merged, PR: "wesnel/nix-config#243", Plan: p.File(), Title: "Ran the loop",
			Summary: "Agents run the loop themselves.\nIt saved a day a week."}
		file := filepath.Join(repo, "brag", "2026.org")
		got, err := c.WriteBrag(o)
		if err != nil || got.File != file || !got.Changed {
			t.Fatalf("WriteBrag: %+v, %v", got, err)
		}
		first := "#+title: Brag sheet 2026\n" +
			"* Ran the loop\n:PROPERTIES:\n:PR: https://github.com/wesnel/nix-config/pull/243\n:REPOSITORY: wesnel/nix-config\n" +
			":PLAN: [[id:" + id + "][Add the loop]]\n:END:\nAgents run the loop themselves.\nIt saved a day a week.\n"
		if data, _ := os.ReadFile(file); string(data) != first {
			t.Errorf("the brag file holds\n%s\nnot\n%s", data, first)
		}
		before, _ := os.Stat(file)
		o.PR = "https://github.com/wesnel/nix-config/pull/243"
		if got, err := c.WriteBrag(o); err != nil || got.Changed {
			t.Errorf("the same pull request again: %+v, %v", got, err)
		}
		if after, _ := os.Stat(file); !os.SameFile(before, after) {
			t.Errorf("the same pull request again replaced the file")
		}
		o.Summary = "Another summary."
		_, err = c.WriteBrag(o)
		refusedSaying(t, err, "already")
		if data, _ := os.ReadFile(file); string(data) != first {
			t.Errorf("a refused entry changed the file:\n%s", data)
		}
		o.PR, o.Title = "wesnel/nix-config#244", "Drew the digest"
		if got, err := c.WriteBrag(o); err != nil || !got.Changed {
			t.Errorf("another pull request: %+v, %v", got, err)
		}
		second := first + "* Drew the digest\n:PROPERTIES:\n:PR: https://github.com/wesnel/nix-config/pull/244\n:REPOSITORY: wesnel/nix-config\n" +
			":PLAN: [[id:" + id + "][Add the loop]]\n:END:\nAnother summary.\n"
		if data, _ := os.ReadFile(file); string(data) != second {
			t.Errorf("the brag file holds\n%s\nnot\n%s", data, second)
		}
	})
	t.Run("written at once by several", func(t *testing.T) {
		c, repo := notesConfig(t)
		p := newPlan(t, c, "loop")
		var wg gosync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := c.WriteBrag(BragOptions{Day: merged, PR: fmt.Sprintf("o/r#%d", i+1), Plan: p.File(), Title: "Change", Summary: "Done."}); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		data, _ := os.ReadFile(filepath.Join(repo, "brag", "2026.org"))
		if n := strings.Count(string(data), "\n:PR: "); n != 8 {
			t.Errorf("%d entries of 8:\n%s", n, data)
		}
	})
	refused := map[string]struct {
		o    BragOptions
		want string
	}{
		"a PR of no form":          {o: BragOptions{PR: "243"}, want: "PR"},
		"no title":                 {o: BragOptions{Title: " "}, want: "title"},
		"a title of two lines":     {o: BragOptions{Title: "Ran\nthe loop"}, want: "line break"},
		"a heading in the summary": {o: BragOptions{Summary: "Done.\n* Not a heading"}, want: "heading"},
		"no summary":               {o: BragOptions{Summary: "\n"}, want: "summary"},
		"no such plan":             {o: BragOptions{Plan: "20261014130500-gone.box.org"}, want: "gone"},
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			c, repo := notesConfig(t)
			p := newPlan(t, c, "loop")
			o := BragOptions{Day: merged, PR: "o/r#1", Plan: p.File(), Title: "Ran the loop", Summary: "Done."}
			if tc.o.PR != "" {
				o.PR = tc.o.PR
			}
			if tc.o.Title != "" {
				o.Title = tc.o.Title
			}
			if tc.o.Summary != "" {
				o.Summary = tc.o.Summary
			}
			if tc.o.Plan != "" {
				o.Plan = tc.o.Plan
			}
			_, err := c.WriteBrag(o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error saying %q", err, tc.want)
			}
			if _, err := os.Lstat(filepath.Join(repo, "brag")); err == nil {
				t.Errorf("a refused entry made the brag directory")
			}
		})
	}
}
