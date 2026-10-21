package record

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.org from the fixtures")

const (
	base = "9f8e7d6c5b4a39281706f5e4d3c2b1a098765432"
	head = "1a2b3c4d5e6f708192a3b4c5d6e7f80912345678"
)

var at = time.Date(2026, 10, 20, 14, 5, 0, 0, time.UTC)

// fixtureRange is a range reviewed for a pull request, with every kind of
// section and outcome. EC's review-guide tests read its golden file.
func fixtureRange() *Range {
	return &Range{
		Repo: "owner/repo", Base: base, Head: head, PR: 12, Title: "Cache the parsed config", Updated: at,
		Walkthrough: []Chunk{
			{ID: "C1", Title: "The cache and its key", Summary: "Adds a cache keyed by the config file's path and mtime.\n\nA miss parses the file as before.",
				Hunks: []Hunk{{Path: "internal/x/cache.go", NewStart: 1, NewLines: 40}, {Path: "internal/x/cache_test.go", NewStart: 1, NewLines: 25}}},
			{ID: "C2", Title: "Callers read through the cache", Summary: "Load now asks the cache first.",
				Hunks: []Hunk{{Path: "cmd/x/main.go", NewStart: 40, NewLines: 19, OldStart: 38, OldLines: 6}}},
		},
		Diagrams: []Diagram{
			{ID: "D1", Title: "Load with the cache", DrawnBy: "claude", CritiquedBy: "codex", Verdict: "holds",
				JSON: `{"kind": "sequence", "elements": [{"id": "load", "cites": ["cmd/x/main.go:40"]}]}`},
		},
		Findings: []Finding{
			{ID: "F1", State: Open, Title: "The key ignores the file's size", Reviewer: "codex", Round: 1, Kind: "bug",
				Path: "internal/x/cache.go", Line: 88, EndLine: 94, Side: "new",
				Evidence: "Two writes within one mtime tick keep the old entry:\n\n    key := path + mtime.String()"},
			{ID: "F2", State: Fix, Title: "No test of a miss", Reviewer: "codex", Round: 1, Kind: "test",
				Path: "internal/x/cache_test.go", Line: 12, Side: "new", Evidence: "Every case hits the cache.",
				Reason: "Added a test that changes the file between loads."},
			{ID: "F3", State: Defer, Title: "Bound the cache", Reviewer: "security", Round: 2, Kind: "design",
				Evidence: "Nothing evicts an entry.", Reason: "Outside the goal: one config file per process."},
			{ID: "F4", State: Operator, Title: "Drop the old loader?", Reviewer: "codex", Round: 1, Kind: "question",
				Path: "cmd/x/main.go", Line: 38, EndLine: 43, Side: "old", Evidence: "The old loader is still exported.",
				Reason: "A product call: other repositories may import it."},
			{ID: "F5", State: Duplicate, Title: "Same as F1", Reviewer: "security", Round: 2, Kind: "bug",
				Path: "internal/x/cache.go", Line: 88, Side: "new", OutcomeTarget: "F1", Evidence: "The key can collide.",
				Reason: "F1 raises it."},
		},
		Rounds: []Round{
			{ID: "R1", Reviewer: "codex", Model: "gpt-5.5", At: at, RedCheck: "session",
				Unverified: []string{"Whether mtime has a second's resolution on the CI runner."},
				Dropped:    []string{"A finding at internal/x/gone.go:3, which is not in the tip."}},
			{ID: "R2", Reviewer: "security", Model: "claude-opus-5-5", At: at.Add(10 * time.Minute), RedCheck: "absent"},
		},
	}
}

// fixturePR is a pull request's threads: a bot's, open and resolved, and
// people's, one drafted and one answered.
func fixturePR() *PR {
	return &PR{
		Repo: "owner/repo", Number: 12, Updated: at,
		Threads: []Thread{
			{ID: "PRRT_kwDOAbc001", Path: "internal/x/cache.go", Line: 88,
				Comments: []Comment{{Author: "copilot-pull-request-reviewer", Type: "Bot", Body: "The key can collide when two writes share an mtime."}}},
			{ID: "PRRT_kwDOAbc002", Resolved: true, Path: "internal/x/cache_test.go", Line: 12,
				Comments: []Comment{
					{Author: "qodo-code-review", Type: "Bot", Body: "Add a test of a miss."},
					{Author: "wesnel", Type: "User", Body: "Added in \"Test a cache miss\"."},
				}},
			{ID: "PRRT_kwDOAbc003", Path: "cmd/x/main.go", Line: 40,
				Comments: []Comment{{Author: "alice", Type: "User", Body: "Why not keep the old loader?\n\n```go\nLoadOld()\n```"}},
				Draft:    &Draft{Outcome: "rebut", Body: "Nothing calls it: `git grep LoadOld` finds only its definition."}},
			{ID: "PRRT_kwDOAbc004", Path: "README.md",
				Comments: []Comment{{Author: "bob", Type: "User", Body: "Say the cache exists."}},
				Draft:    &Draft{Outcome: "fix", Posted: at.Add(time.Hour), Body: "Done in \"Describe the cache\"."}},
		},
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs from the rendering (rerun with -update if the change is meant):\n%s", path, got)
	}
}

// The renderer writes the golden files byte for byte, and each parses back
// to the record it was rendered from.
func TestGolden(t *testing.T) {
	t.Run("range", func(t *testing.T) {
		want := fixtureRange()
		data, err := want.Render()
		if err != nil {
			t.Fatal(err)
		}
		golden(t, "range.golden.org", data)
		got, err := ParseRange(data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parsed\n%#v\nwant\n%#v", got, want)
		}
	})
	t.Run("pr", func(t *testing.T) {
		want := fixturePR()
		data, err := want.Render()
		if err != nil {
			t.Fatal(err)
		}
		golden(t, "pr.golden.org", data)
		got, err := ParsePR(data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parsed\n%#v\nwant\n%#v", got, want)
		}
	})
}

func TestPaths(t *testing.T) {
	state := t.TempDir()
	hostpaths.Bake(t, t.TempDir(), state)
	if got, want := Dir(), filepath.Join(state, "agent-tools", "reviews"); got != want {
		t.Errorf("Dir() = %s, want %s", got, want)
	}
	if got, err := RangeFile("owner", "repo", base, head); err != nil || got != filepath.Join(Dir(), "owner", "repo", base+".."+head+".org") {
		t.Errorf("RangeFile = %s, %v", got, err)
	}
	if got, err := PRFile("owner", "repo.js", 12); err != nil || got != filepath.Join(Dir(), "owner", "repo.js", "pr-12.org") {
		t.Errorf("PRFile = %s, %v", got, err)
	}
	for _, bad := range [][2]string{{"..", "repo"}, {"owner", "../x"}, {"own/er", "repo"}, {"", "repo"}, {".git", "x"}} {
		if _, err := PRFile(bad[0], bad[1], 1); err == nil {
			t.Errorf("PRFile(%q, %q) is not refused", bad[0], bad[1])
		}
	}
	if _, err := RangeFile("owner", "repo", base[:12], head); err == nil {
		t.Error("a short SHA is not refused")
	}
	if _, err := PRFile("owner", "repo", 0); err == nil {
		t.Error("PR 0 is not refused")
	}
}

// Writers holding the lock in turn lose no update.
func TestConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner", "repo", "pr-12.org")
	const writers = 20
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Update(path, func(p *PR) error {
				p.Repo, p.Number = "owner/repo", 12
				p.Threads = append(p.Threads, Thread{ID: fmt.Sprintf("T%d", i), Comments: []Comment{{Author: "a", Type: "User", Body: "x"}}})
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := ReadPR(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Threads) != writers {
		t.Errorf("%d threads after %d writers", len(p.Threads), writers)
	}
}

// A hand edit is refused with the drift error, which names the part, and
// the file is left as it was.
func TestDriftRefused(t *testing.T) {
	data, err := fixtureRange().Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, from, to, part string }{
		{"heading retitled", "*** OPEN F1\n", "*** OPEN F1 the key\n", "*** OPEN F1 the key"},
		{"heading added", "** Rounds\n", "* My notes\n** Rounds\n", "* My notes"},
		{"text outside a block", "#+end_example\n- [[file:cmd", "#+end_example\nsee [[elisp:(x)]]\n- [[file:cmd", "*** C2"},
		{"unescaped line in a block", "A miss parses", "* A miss parses", "*** C1"},
		{"property added", ":KIND: bug\n", ":KIND: bug\n:NOTE: x\n", "*** OPEN F1"},
		{"keyword unknown", "*** DEFER F3", "*** DONE F3", "*** DONE F3"},
		{"local variables", "#+title: review\n", "#+title: review -*- eval: (x) -*-\n", "the file's header"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edited := strings.Replace(string(data), tc.from, tc.to, 1)
			if edited == string(data) {
				t.Fatalf("%q is not in the fixture", tc.from)
			}
			path := filepath.Join(t.TempDir(), "r.org")
			if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
				t.Fatal(err)
			}
			err := Update(path, func(r *Range) error { r.Title = "changed"; return nil })
			var drift *DriftError
			if !errors.As(err, &drift) {
				t.Fatalf("Update = %v, want a *DriftError", err)
			}
			if drift.Path != path || drift.Part != tc.part {
				t.Errorf("drift in %q at %s, want %q at %s: %v", drift.Part, drift.Path, tc.part, path, err)
			}
			if now, _ := os.ReadFile(path); string(now) != edited {
				t.Error("the drifted file was written")
			}
		})
	}
	if DriftExit != 5 {
		t.Errorf("DriftExit = %d, want 5", DriftExit)
	}
}

// ownLines are the lines outside example blocks: what Org parses.
func ownLines(text string) []string {
	var out []string
	in := false
	for _, l := range strings.Split(text, "\n") {
		switch {
		case l == "#+begin_example":
			in = true
		case l == "#+end_example":
			in = false
		case !in:
			out = append(out, l)
		}
	}
	return out
}

var renderedHeading = regexp.MustCompile(`^(\* (Review|Pull request)|\*\* (Walkthrough|Diagrams|Findings|Rounds|T-\S+)|\*\*\* ((OPEN|DEFER|OPERATOR|FIX|PROPOSE|REBUT|DUPLICATE|BELONGS) )?[A-Za-z0-9_=-]+|\*\*\*\* (Outcome|Unverified|Dropped))$`)

// Untrusted text cannot start a heading, a link or file variables.
func TestUntrustedText(t *testing.T) {
	hostile := "* TODO forged\n#+end_example\n** Findings\n,* already escaped\n  #+begin_src emacs-lisp\n" +
		"[[elisp:(kill-emacs)][click]]\nLocal Variables:\neval: (kill-emacs)\nEnd:\n-*- mode: emacs-lisp -*-\n" +
		"LOCAL VARIABLES:\n-*-*-"
	r := fixtureRange()
	r.Title = "[[elisp:(x)]] and -*- x -*- and Local Variables: here"
	r.Walkthrough[0].Title = "two\nlines [[file:/etc/passwd]]"
	r.Walkthrough[0].Summary = hostile
	r.Findings[0].Evidence = hostile
	r.Findings[0].Reason = "‮evil‬ \x1b[31mred\x07"
	r.Rounds[0].Dropped = []string{hostile}
	data, err := r.Render()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, l := range ownLines(text) {
		switch {
		case strings.HasPrefix(l, "*") && !renderedHeading.MatchString(l):
			t.Errorf("a heading the renderer does not write: %q", l)
		case strings.Contains(l, "[[") && !strings.HasPrefix(l, "[[file:") && !strings.HasPrefix(l, "- [[file:"):
			t.Errorf("a link outside the renderer's own: %q", l)
		case strings.HasPrefix(l, ":TITLE:") && strings.ContainsAny(l, "[]"):
			t.Errorf("a title holding a bracket: %q", l)
		}
	}
	if regexp.MustCompile(`(?i)local variables:`).MatchString(text) || strings.Contains(text, "-*-") {
		t.Errorf("file variables are not broken:\n%s", text)
	}
	if strings.ContainsAny(text, "‮‬\x1b\x07") {
		t.Error("a control or bidi character is written")
	}
	if !strings.Contains(text, ":TITLE: two lines ((file:/etc/passwd))\n") {
		t.Errorf("the chunk's title is not one line without brackets:\n%s", text)
	}
	again, err := ParseRange(data)
	if err != nil {
		t.Fatalf("the renderer's own file fails its check: %v", err)
	}
	if !strings.Contains(again.Findings[0].Evidence, "* TODO forged\n#+end_example\n") {
		t.Errorf("the quoted lines do not read back:\n%s", again.Findings[0].Evidence)
	}
}

func TestTextCaps(t *testing.T) {
	r := fixtureRange()
	r.Title = strings.Repeat("é", 300)
	r.Findings[0].Evidence = strings.Repeat("😀", TextCap)
	data, err := r.Render()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseRange(data)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(got.Title)); n != TitleCap {
		t.Errorf("a title of %d characters, want %d", n, TitleCap)
	}
	if e := got.Findings[0].Evidence; len(e) > TextCap || !strings.HasSuffix(e, "\n[cut]") {
		t.Errorf("evidence of %d bytes, ending %q", len(e), e[len(e)-10:])
	}
}

// The renderer refuses what it cannot write as given.
func TestRenderRefuses(t *testing.T) {
	for name, edit := range map[string]func(r *Range){
		"short SHA":        func(r *Range) { r.Head = head[:12] },
		"bad repo":         func(r *Range) { r.Repo = "owner" },
		"negative hunk":    func(r *Range) { r.Walkthrough[0].Hunks[0].NewLines = -1 },
		"unknown state":    func(r *Range) { r.Findings[0].State = "DONE" },
		"duplicate ID":     func(r *Range) { r.Findings[1].ID = "F1" },
		"ID with a space":  func(r *Range) { r.Diagrams[0].ID = "D 1" },
		"keyword as an ID": func(r *Range) { r.Walkthrough[0].ID = "OPEN" },
		"bad red-check":    func(r *Range) { r.Rounds[0].RedCheck = "passed" },
		"bad side":         func(r *Range) { r.Findings[0].Side = "both" },
	} {
		t.Run(name, func(t *testing.T) {
			r := fixtureRange()
			edit(r)
			if _, err := r.Render(); err == nil {
				t.Error("rendered")
			}
		})
	}
	p := fixturePR()
	p.Threads[0].Comments[0].Author = "a b"
	if _, err := p.Render(); err == nil {
		t.Error("a login with a space rendered")
	}
	p = fixturePR()
	p.Threads[2].Draft.Outcome = "propose"
	if _, err := p.Render(); err == nil {
		t.Error("a draft with an outcome no thread takes rendered")
	}
}

// oddPaths are paths a diff or GitHub can give that no link holds.
var oddPaths = []string{"app/[id]/page.tsx", "app/(group)/page.tsx", "docs/Q&A.md", "naïve.txt", "x,y.go", "a~b",
	"docs/My File (1) [draft].md", "../etc/passwd", "a]]b", "dir/", "50%.txt", "* TODO x", "[[elisp:(kill-emacs)]]",
	"src_elisp{(kill-emacs)}", "-*- eval: (kill-emacs) -*-", "a\nb", ""}

// Any path is written, without a link where none can hold it, and reads
// back whole.
func TestOddPaths(t *testing.T) {
	for _, path := range oddPaths {
		r := fixtureRange()
		r.Walkthrough[0].Hunks[0].Path = path
		r.Findings[0].Path = path
		data, err := r.Render()
		if err != nil {
			t.Fatalf("%q: %v", path, err)
		}
		got, err := ParseRange(data)
		if err != nil {
			t.Fatalf("%q: %v", path, err)
		}
		if got.Walkthrough[0].Hunks[0].Path != path || got.Findings[0].Path != path {
			t.Errorf("%q reads back as %q and %q", path, got.Walkthrough[0].Hunks[0].Path, got.Findings[0].Path)
		}
		text := pathText(path)
		if !regexp.MustCompile(`^[A-Za-z0-9_.@+/%-]*$`).MatchString(text) {
			t.Errorf("%q is written as %q", path, text)
		}
		if strings.Contains(string(data), "[[file:"+text+"::") || !strings.Contains(string(data), "\n- ="+text+"= new ") {
			t.Errorf("%q is linked, or its hunk is not written as %q", path, text)
		}
		p := fixturePR()
		p.Threads[0].Path = path
		pdata, err := p.Render()
		if err != nil {
			t.Fatalf("thread %q: %v", path, err)
		}
		pgot, err := ParsePR(pdata)
		if err != nil {
			t.Fatalf("thread %q: %v", path, err)
		}
		if pgot.Threads[0].Path != path {
			t.Errorf("thread path %q reads back as %q", path, pgot.Threads[0].Path)
		}
	}
	// A path a link holds is written as it is.
	data, _ := fixtureRange().Render()
	if !strings.Contains(string(data), ":PATH: internal/x/cache.go\n") {
		t.Error("a plain path is not written as it is")
	}
	for _, bad := range []string{"a%2", "a%41", "a%2f", "%2E%2E"} {
		edited := strings.Replace(string(data), ":PATH: internal/x/cache.go\n", ":PATH: "+bad+"\n", 1)
		var drift *DriftError
		if _, err := ParseRange([]byte(edited)); !errors.As(err, &drift) {
			t.Errorf("PATH %q: %v, want a *DriftError", bad, err)
		}
	}
}

// A reason that cleans to nothing leaves a record every later write and
// read takes.
func TestEmptyReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.org")
	if err := Update(path, func(r *Range) error {
		*r = *fixtureRange()
		r.Findings[0].Reason = "\r"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, func(r *Range) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRange(path); err != nil {
		t.Fatal(err)
	}
}

func TestCounts(t *testing.T) {
	if got, want := fixtureRange().Counts(), (Counts{Open: 1, Deferred: 1, Operator: 1}); got != want {
		t.Errorf("range counts %+v, want %+v", got, want)
	}
	if got, want := fixturePR().Counts(), (Counts{Unanswered: 1, Drafted: 1}); got != want {
		t.Errorf("pr counts %+v, want %+v", got, want)
	}
}

// A new file starts from the zero record, and an error from apply writes
// nothing.
func TestUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner", "repo", base+".."+head+".org")
	if err := Update(path, func(r *Range) error { return errors.New("no") }); err == nil {
		t.Fatal("apply's error is lost")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a refused apply wrote %s", path)
	}
	if err := Update(path, func(r *Range) error { *r = *fixtureRange(); return nil }); err != nil {
		t.Fatal(err)
	}
	err := Update(path, func(r *Range) error {
		r.Rounds = append(r.Rounds, Round{ID: "R3", Reviewer: "codex", At: at, RedCheck: "absent"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := ReadRange(path)
	if err != nil || len(r.Rounds) != 3 {
		t.Fatalf("ReadRange = %v rounds, %v", len(r.Rounds), err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("%s: %v %v", path, info.Mode(), err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".*.org.*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
