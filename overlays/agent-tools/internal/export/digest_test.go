package export

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/plans"
)

// firstChange is d's first change, failing the test where it has none.
func firstChange(t *testing.T, d Digest) Change {
	t.Helper()
	if len(d.Changes) == 0 {
		t.Fatalf("no changes: %+v", d)
	}
	return d.Changes[0]
}

// digestFrom is the one digest TodayOf reads from a digest file holding
// body, dated the day before day.
func digestFrom(t *testing.T, body string) Digest {
	t.Helper()
	repo := t.TempDir()
	c := plans.Config{Dir: filepath.Join(repo, "agents"), Host: "box"}
	write(t, filepath.Join(repo, "digests", "2026", "10-13-demo.org"), body)
	got, err := TodayOf(c, day)
	if err != nil || len(got.Digests) != 1 {
		t.Fatalf("today %+v, %v", got, err)
	}
	return got.Digests[0]
}

// change is a change with no steps, diagrams or links but those given
// after it.
func change(title, repo string, number int, summary string) Change {
	return Change{Title: title, Repo: repo, Number: number, Summary: summary, Steps: []DemoStep{},
		Diagrams: []DigestDiagram{}, Links: []DigestLink{}}
}

const wellFormed = `#+title: Demo of the week
Two changes reached main.

* Draw the digest
:PROPERTIES:
:PLAN: [[id:867f8a68-d069-4a03-9d20-a2c14ae0c2d5][Builds on line]]
:PR: wesnel/nix-config#250
:END:
The Today panel draws the digest.

Export the documents:
#+begin_src sh
plan-dash export
#+end_src

#+begin_example
{"documents": 3}
#+end_example

#+caption: How the digest reaches the page
#+begin_src mermaid
flowchart LR
    N1["export"]
    N2["page"]
    N1 -->|"today/digest"| N2
#+end_src

Recorded: [[https://github.com/user-attachments/assets/1234][the panel drawn]] and [[https://github.com/user-attachments/assets/5678]].

* Second change
:PROPERTIES:
:PR: https://github.com/wesnel/emacs-config/pull/12
:END:
#+begin_src sh :results output
,* not a heading
#+end_src
`

// A digest in the demo digest's format is parsed into its changes, each
// with its pull request, its steps with their results, its diagrams with
// their captions and its https links; the raw text stays as it was.
func TestDigest(t *testing.T) {
	got := digestFrom(t, wellFormed)
	first := change("Draw the digest", "wesnel/nix-config", 250,
		"The Today panel draws the digest.\n\nRecorded: the panel drawn and https://github.com/user-attachments/assets/5678.")
	first.Steps = []DemoStep{{Text: "Export the documents:", Command: "plan-dash export", Result: `{"documents": 3}`}}
	first.Diagrams = []DigestDiagram{{Caption: "How the digest reaches the page",
		Source: "flowchart LR\n    N1[\"export\"]\n    N2[\"page\"]\n    N1 -->|\"today/digest\"| N2\n"}}
	first.Links = []DigestLink{{URL: "https://github.com/user-attachments/assets/1234", Caption: "the panel drawn"},
		{URL: "https://github.com/user-attachments/assets/5678", Caption: "https://github.com/user-attachments/assets/5678"}}
	second := change("Second change", "wesnel/emacs-config", 12, "")
	second.Steps = []DemoStep{{Command: "* not a heading"}}
	want := Digest{Kind: "demo", Text: strings.TrimSpace(wellFormed), Summary: "Two changes reached main.",
		Changes: []Change{first, second}, Dropped: []Dropped{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("digest\n%+v\nwant\n%+v", got, want)
	}
}

// What the digest's format cannot show is dropped with the line it starts
// on and why, and the rest of the digest is still read.
func TestDigestMalformed(t *testing.T) {
	head := func(pr string) string { return "* A\n:PROPERTIES:\n:PR: " + pr + "\n:END:\n" }
	for _, c := range []struct {
		name, body string
		want       []Change
		dropped    []Dropped
	}{
		{"a block never closed", head("o/r#1") + "#+begin_src sh\necho a\n* B\n:PROPERTIES:\n:PR: o/r#2\n:END:\nShown.\n",
			[]Change{change("A", "o/r", 1, ""), change("B", "o/r", 2, "Shown.")},
			[]Dropped{{5, "step", "its block is never closed"}}},
		{"a result never closed", head("o/r#1") + "#+begin_src sh\necho a\n#+end_src\n#+begin_example\na\n",
			[]Change{change("A", "o/r", 1, "")},
			[]Dropped{{5, "step", "its result's block is never closed"}}},
		{"a link that is not https", head("o/r#1") +
			"See [[http://example.com/a][the page]], [[file:/etc/hosts][hosts]], [[https://me@example.com/][mine]] and [[javascript:alert(1)][this]].\n",
			[]Change{change("A", "o/r", 1, "See the page, hosts, mine and this.")},
			[]Dropped{{5, "link", "not an https URL"}, {5, "link", "not an https URL"}, {5, "link", "not an https URL"},
				{5, "link", "not an https URL"}}},
		{"Mermaid outside the page's grammar", head("o/r#1") +
			"#+caption: Not drawn\n#+begin_src mermaid\ngraph TD\n    A-->B\n    click A \"https://example.com\"\n#+end_src\nAfter.\n",
			[]Change{change("A", "o/r", 1, "After.")},
			[]Dropped{{6, "diagram", "not in the one grammar the page draws"}}},
		{"a PR property of another form", head("soon"),
			[]Change{change("A", "", 0, "")},
			[]Dropped{{3, "pr", "not OWNER/NAME#N or a pull request's GitHub URL"}}},
		{"a result with no step", head("o/r#1") + "#+begin_example\nx\n#+end_example\n",
			[]Change{change("A", "o/r", 1, "")},
			[]Dropped{{5, "block", "a result with no step before it"}}},
		{"a block of another kind", head("o/r#1") + "#+begin_src python\nprint(1)\n#+end_src\n#+begin_quote\nq\n#+end_quote\n",
			[]Change{change("A", "o/r", 1, "")},
			[]Dropped{{5, "block", "not a block the digest's format has"}, {8, "block", "not a block the digest's format has"}}},
		{"a link before the first change", "See [[https://example.com/a][this]].\n" + head("o/r#1"),
			[]Change{change("A", "o/r", 1, "")},
			[]Dropped{{1, "link", "outside a change, where the page shows no links"}}},
		{"a property drawer never closed", "* A\n:PROPERTIES:\n:PR: o/r#1\nHidden.\n",
			[]Change{change("A", "o/r", 1, "")},
			[]Dropped{{2, "block", "its property drawer is never closed"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := digestFrom(t, c.body)
			if !reflect.DeepEqual(got.Changes, c.want) || !reflect.DeepEqual(got.Dropped, c.dropped) {
				t.Errorf("changes %+v\ndropped %+v\nwant %+v\n%+v", got.Changes, got.Dropped, c.want, c.dropped)
			}
		})
	}
}

// A digest with no level-1 heading carrying a PR or PLAN property is text
// alone, which the page shows as it is.
func TestDigestFreeText(t *testing.T) {
	got := digestFrom(t, "* Yesterday\n- Shipped the loop.\n")
	want := Digest{Kind: "demo", Text: "* Yesterday\n- Shipped the loop.", Changes: []Change{}, Dropped: []Dropped{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("digest %+v, want %+v", got, want)
	}
}

// A digest over what the export reads, or a change over what it shows,
// keeps what fits and names what does not; a long result is cut; and the
// document stays within maxDoc.
func TestDigestCaps(t *testing.T) {
	t.Run("steps", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("* A\n:PROPERTIES:\n:PR: o/r#1\n:END:\n")
		for i := range maxDemoSteps + 1 {
			fmt.Fprintf(&b, "#+begin_src sh\necho %d\n#+end_src\n", i)
		}
		got := digestFrom(t, b.String())
		steps := firstChange(t, got).Steps
		if len(steps) != 20 || steps[19].Command != "echo 19" {
			t.Errorf("%d steps: %+v", len(steps), steps)
		}
		if want := []Dropped{{5 + 3*20, "step", "over the 20 steps a change shows"}}; !reflect.DeepEqual(got.Dropped, want) {
			t.Errorf("dropped %+v, want %+v", got.Dropped, want)
		}
	})
	t.Run("lists", func(t *testing.T) {
		head := "* A\n:PROPERTIES:\n:PR: o/r#1\n:END:\n"
		mermaid := "#+begin_src mermaid\nflowchart LR\n    N1[\"a\"]\n#+end_src\n"
		var big strings.Builder
		big.WriteString("#+begin_src mermaid\nflowchart LR\n")
		for big.Len() <= 20000 {
			big.WriteString("    N1[\"" + strings.Repeat("a", 50) + "\"]\n")
		}
		big.WriteString("#+end_src\n")
		var links strings.Builder
		for range maxDigestLinks + 1 {
			links.WriteString("[[https://example.com/][x]] ")
		}
		var changes strings.Builder
		for range maxChanges + 1 {
			changes.WriteString(head)
		}
		for _, c := range []struct {
			name, body string
			want       Dropped
			count      func(Digest) int
			kept       int
		}{
			{"diagrams", head + strings.Repeat(mermaid, maxDigestDiagrams+1),
				Dropped{5 + 4*5, "diagram", "over the 5 diagrams a change shows"},
				func(d Digest) int { return len(firstChange(t, d).Diagrams) }, 5},
			{"a diagram's source", head + big.String(), Dropped{5, "diagram", "over the 20000 bytes the page draws"},
				func(d Digest) int { return len(firstChange(t, d).Diagrams) }, 0},
			{"links", head + links.String() + "\n", Dropped{5, "link", "over the 20 links a change shows"},
				func(d Digest) int { return len(firstChange(t, d).Links) }, 20},
			{"changes", changes.String(), Dropped{1 + 4*20, "change", "over the 20 changes a digest shows"},
				func(d Digest) int { return len(d.Changes) }, 20},
		} {
			t.Run(c.name, func(t *testing.T) {
				got := digestFrom(t, c.body)
				if n := c.count(got); n != c.kept || !reflect.DeepEqual(got.Dropped, []Dropped{c.want}) {
					t.Errorf("%d kept, want %d; dropped %+v, want %+v", n, c.kept, got.Dropped, c.want)
				}
			})
		}
		var bad strings.Builder
		bad.WriteString(head)
		for range maxDropped + 5 {
			bad.WriteString("#+begin_quote\n#+end_quote\n")
		}
		if got := digestFrom(t, bad.String()); len(got.Dropped) != 30 {
			t.Errorf("%d dropped, want 30", len(got.Dropped))
		}
	})
	t.Run("a long result", func(t *testing.T) {
		got := digestFrom(t, "* A\n:PROPERTIES:\n:PR: o/r#1\n:END:\n#+begin_src sh\nyes\n#+end_src\n#+begin_example\n"+
			strings.Repeat("y\n", 3000)+"#+end_example\n")
		steps := firstChange(t, got).Steps
		if len(steps) != 1 || len([]rune(steps[0].Result)) != 4000 {
			t.Errorf("steps %d, want one with a result of 4000 runes", len(steps))
		}
	})
	t.Run("the file", func(t *testing.T) {
		wide := strings.Repeat("😀", 70<<10/4)
		body := "* A\n:PROPERTIES:\n:PR: o/r#1\n:END:\n" + wide + "\n* B\n:PROPERTIES:\n:PR: o/r#2\n:END:\n"
		got := digestFrom(t, body)
		if a := firstChange(t, got); len(got.Changes) != 1 || a.Title != "A" || len([]rune(a.Summary)) != 2000 {
			t.Errorf("changes %d, the first %q with %d runes of summary", len(got.Changes), a.Title, len([]rune(a.Summary)))
		}
		if want := []Dropped{{0, "digest", "over the 64 KiB the export reads; the rest is not shown"}}; !reflect.DeepEqual(got.Dropped, want) {
			t.Errorf("dropped %+v, want %+v", got.Dropped, want)
		}
		out := t.TempDir()
		if err := Write(out, Export{Today: Today{Digests: []Digest{got}}}); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(filepath.Join(out, "today", "digest.json")); err != nil || info.Size() > maxDoc {
			t.Errorf("today/digest of %v bytes (%v)", info, err)
		}
	})
}
