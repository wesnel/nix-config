package digest

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/export"
	"github.com/wesnel/agent-tools/internal/usage"
)

func slot(t *testing.T, at string) Slot {
	t.Helper()
	s, err := ParseSlot(at, "America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseSlot(t *testing.T) {
	for _, c := range [][2]string{{"7:30am", "America/Los_Angeles"}, {"25:00", "America/Los_Angeles"},
		{"07:30", "Mars/Olympus"}, {"07:30", ""}, {"07:30", "Local"}} {
		if _, err := ParseSlot(c[0], c[1]); err == nil {
			t.Errorf("%q: no error", c)
		}
	}
}

// A run writes the digest of the newest weekday slot, counting changes
// since the weekday slot before it, unless it starts on a weekend in the
// slot's zone or more than Late after that slot.
func TestDue(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	at := func(day, clock string) time.Time {
		t.Helper()
		v, err := time.ParseInLocation("2006-01-02 15:04", day+" "+clock, la)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	standup := slot(t, "07:30")
	for _, c := range []struct {
		name               string
		s                  Slot
		now, due, previous time.Time
	}{
		{"on the slot", standup, at("2026-10-14", "07:30"), at("2026-10-14", "07:30"), at("2026-10-13", "07:30")},
		{"in UTC", standup, at("2026-10-14", "07:31").UTC(), at("2026-10-14", "07:30"), at("2026-10-13", "07:30")},
		{"at the latest", standup, at("2026-10-14", "11:30"), at("2026-10-14", "07:30"), at("2026-10-13", "07:30")},
		{"too late", standup, at("2026-10-14", "11:31"), time.Time{}, time.Time{}},
		{"before the slot", standup, at("2026-10-14", "07:29"), time.Time{}, time.Time{}},
		{"Monday, since Friday", standup, at("2026-10-12", "08:00"), at("2026-10-12", "07:30"), at("2026-10-09", "07:30")},
		{"Monday's boot, Friday's slot missed", standup, at("2026-10-12", "06:00"), time.Time{}, time.Time{}},
		{"Saturday", standup, at("2026-10-17", "07:30"), time.Time{}, time.Time{}},
		{"Sunday", standup, at("2026-10-18", "09:00"), time.Time{}, time.Time{}},
		// Daylight saving time ends on 1 November 2026.
		{"across the change of offset", standup, at("2026-11-02", "07:45"), at("2026-11-02", "07:30"), at("2026-10-30", "07:30")},
		// Friday evening in Los Angeles is Saturday in UTC.
		{"weekday in the zone", slot(t, "20:00"), at("2026-10-16", "21:00").UTC(), at("2026-10-16", "20:00"), at("2026-10-15", "20:00")},
		// Within Late of Friday's slot, but Saturday in the zone.
		{"Saturday, soon after Friday's slot", slot(t, "20:00"), at("2026-10-17", "00:00"), time.Time{}, time.Time{}},
	} {
		due, previous, why, ok := c.s.Due(c.now)
		if ok != !c.due.IsZero() || !due.Equal(c.due) || !previous.Equal(c.previous) || ok == (why != "") {
			t.Errorf("%s: %v, %v, %q, %v; want %v, %v", c.name, due, previous, why, ok, c.due, c.previous)
		}
	}
	if due, _, _, _ := standup.Due(at("2026-11-02", "07:45")); due.Format("-07:00") != "-08:00" {
		t.Errorf("Monday 2 November's slot: %v", due)
	}
	if f, err := File("standup", "devbox", at("2026-10-14", "07:30")); err != nil || f != "digests/2026/10-14-standup.devbox.org" {
		t.Errorf("file %q, %v", f, err)
	}
	for _, c := range [][2]string{{"standup", "dev.box"}, {"standup", "../x"}, {"standup", ""}, {"Standup/x", "devbox"}} {
		if f, err := File(c[0], c[1], at("2026-10-14", "07:30")); err == nil {
			t.Errorf("%q: %q", c, f)
		}
	}
}

// The standup lists plans changed since the previous slot apart from the
// rest, with their ready steps; pull requests by key, state and link, never
// by title; quota and sync. TestInert covers plan text made of Org syntax.
func TestStandup(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	slotAt := time.Date(2026, 10, 14, 7, 30, 0, 0, la)
	in := Inputs{At: slotAt, Previous: slotAt.AddDate(0, 0, -1), Now: slotAt.Add(time.Minute),
		Export: export.Export{
			Plans: []export.Plan{
				{ID: "2026-10-13-loop.devbox", Title: "Run the loop", Status: "working",
					Host: "devbox", Updated: "2026-10-14T06:00:00-07:00", Steps: []export.Step{
						{ID: "S1", Title: "Write it", Keyword: "DONE"},
						{ID: "S2", Title: "Test it\n  twice", Keyword: "TODO", Ready: true},
						{ID: "S3", Title: "Ship it", Keyword: "TODO"}},
					Questions: []export.Question{{ID: "Q1"}}},
				{ID: "2026-10-01-old.ngrok", Title: "Old work", Status: "waiting", Host: "ngrok",
					Updated: "2026-10-12T10:00:00Z"},
				{ID: "../escape", Title: "Not a plan"},
			},
			Failed: []string{"2026-10-02-broken.devbox"},
			GitHub: export.GitHub{At: "2026-10-14T14:29:00Z", MineTotal: 1, RequestsTotal: 2,
				Mine: []export.PR{{Repo: "o/r", Number: 7, Title: "SECRET-title", Draft: true, CI: "failure",
					Review: "none", Unresolved: 1}},
				Requests: []export.Request{{Repo: "p/q", Number: 3, Title: "SECRET-request", Team: true,
					Requested: "2026-10-13T16:00:00Z", CI: "success", Record: "yes"}}},
			Sync: export.Sync{Host: "devbox", Repo: true, Pushed: "2026-10-14T13:00:00Z", Unsynced: true},
		},
		Quota: []usage.Reading{{Provider: "claude", ObservedAt: slotAt.Add(-time.Hour).Unix(),
			Windows: []usage.Window{{Name: "5h", Used: 42.4, ResetsAt: slotAt.Add(time.Hour).Unix()}}}},
	}
	got := Standup(in)
	for _, want := range []string{
		"#+title: Standup 2026-10-14 Wed\n",
		"Every host's plans not halted, from the notes repository as of [2026-10-14 Wed 07:31]. 1 could not be read.\n",
		"** Changed since [2026-10-13 Tue 07:30]\n- =2026-10-13-loop.devbox= :: working, on =devbox=, " +
			"updated [2026-10-14 Wed 06:00]; steps 1 of 3 done; 1 question open for the operator\n" +
			"  #+begin_example\n  Run the loop\n  ready: Test it twice\n  #+end_example\n",
		"** Unchanged\n- =2026-10-01-old.ngrok= :: waiting, on =ngrok=, updated [2026-10-12 Mon 03:00]; steps 0 of 0 done\n" +
			"  #+begin_example\n  Old work\n  #+end_example\n",
		"GitHub as of [2026-10-14 Wed 07:29].\n",
		"- [[https://github.com/o/r/pull/7][o/r#7]] :: draft, CI failure, review none, 1 unresolved thread\n",
		"- [[https://github.com/p/q/pull/3][p/q#3]] :: of a team since [2026-10-13 Tue 09:00], CI success, reviewed by pr-loop: yes\n1 of 2 shown.\n",
		"- claude :: observed [2026-10-14 Wed 06:30]; 5h 42% used, resets [2026-10-14 Wed 08:30]\n",
		"The plans of =devbox= last reached the remote [2026-10-14 Wed 06:00], changes not yet pushed; last fetched never.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"SECRET", "Not a plan"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q in:\n%s", bad, got)
		}
	}

	in.Export.GitHub = export.GitHub{}
	in.Quota = nil
	in.Export.Sync = export.Sync{Host: "devbox"}
	got = Standup(in)
	for _, want := range []string{"GitHub: no data, the fetch failed.\n", "No reading on this host with a window yet to reset.\n",
		"The plans directory of =devbox= is in no notes repository.\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
}

// payloads are plan text made of Org syntax: links bracketed and plain,
// inline source blocks and calls, macros, export snippets, headlines,
// keywords and blocks, drawers, tables, lists, footnotes, targets, LaTeX,
// planning lines, comments, fixed-width lines, timestamps and emphasis.
var payloads = []string{
	`[[[elisp:(progn (write-region "x" nil "/tmp/dt-pwned") (message "pwned"))][click]]]`,
	`[[elisp:(kill-emacs)][click]]`,
	`[[file:/etc/passwd]]`,
	`shell:touch${IFS}/tmp/dt-pwned`,
	`elisp:ignore`,
	`file:/etc/passwd`,
	`https://example.com/x`,
	`src_emacs-lisp{(write-region "x" nil "/tmp/dt-pwned")}`,
	`src_sh[:results raw]{touch /tmp/dt-pwned}`,
	`call_foo()`,
	`{{{date}}}`,
	`@@html:<script>alert(1)</script>@@`,
	"* TODO a headline",
	"** DONE another",
	",* an escaped headline",
	"#+TITLE: another title",
	"#+begin_src emacs-lisp",
	"#+end_example",
	",#+end_example",
	"#+begin_example",
	"#+CALL: foo()",
	"#+INCLUDE: \"/etc/passwd\"",
	":PROPERTIES:",
	":END:",
	":LOGBOOK:",
	"| a | b |",
	"|-+-|",
	"- item",
	"1. item",
	"[fn:1] a footnote",
	"<<target>>",
	"<<<radio>>>",
	`\alpha $x$ \(y\)`,
	"CLOCK: [2026-01-01 Thu 10:00]",
	"SCHEDULED: <2026-01-01 Thu>",
	"# a comment",
	": fixed width",
	"<2026-10-14 Wed>",
	"*bold* /italic/ =verbatim= ~code~ a_b^c",
	"line one\n* TODO two\n#+begin_src",
}

// corpus is a standup whose plans' titles and ready steps are payloads.
func corpus() Inputs {
	la, _ := time.LoadLocation("America/Los_Angeles")
	at := time.Date(2026, 10, 14, 7, 30, 0, 0, la)
	in := Inputs{At: at, Previous: at.AddDate(0, 0, -1), Now: at}
	for i, p := range payloads {
		in.Export.Plans = append(in.Export.Plans, export.Plan{ID: fmt.Sprintf("2026-10-13-p%d.devbox", i), Title: p,
			Status: "working", Host: "devbox", Updated: "2026-10-14T06:00:00-07:00",
			Steps: []export.Step{{ID: "S1", Title: p, Keyword: "TODO", Ready: true}}})
	}
	return in
}

// The digest of the corpus holds every payload. Where DIGEST_INERT_OUT
// names a file, it is written there: the flake's digest-inert check runs
// this test so, then testdata/inert.el on the file, which shows that Org
// parses none of the plan text.
func TestInert(t *testing.T) {
	got := Standup(corpus())
	if out := os.Getenv("DIGEST_INERT_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range payloads {
		if !strings.Contains(got, strings.Join(strings.Fields(p), " ")) {
			t.Errorf("%q is not in the digest", p)
		}
	}
}

// A digest of every list at its cap, each text at export's cap in
// four-byte runes, fits in a document.
func TestSize(t *testing.T) {
	long := strings.Repeat("😀", 2000)
	in := corpus()
	in.Export.Plans = nil
	for i := range 200 {
		p := export.Plan{ID: fmt.Sprintf("2026-10-13-p%d.devbox", i), Title: long[:200*4], Status: "working", Host: "devbox"}
		for j := range 100 {
			p.Steps = append(p.Steps, export.Step{ID: fmt.Sprintf("S%d", j), Title: long[:200*4], Keyword: "TODO", Ready: true})
		}
		in.Export.Plans = append(in.Export.Plans, p)
	}
	in.Export.GitHub = export.GitHub{At: "2026-10-14T14:29:00Z", MineTotal: 1000, RequestsTotal: 1000}
	for i := range 100 {
		in.Export.GitHub.Mine = append(in.Export.GitHub.Mine, export.PR{Repo: strings.Repeat("o", 39) + "/" + strings.Repeat("r", 100), Number: 100000 + i})
		in.Export.GitHub.Requests = append(in.Export.GitHub.Requests, export.Request{Repo: strings.Repeat("o", 39) + "/" + strings.Repeat("r", 100), Number: 100000 + i})
	}
	got := Standup(in)
	if len(got) > 500<<10 {
		t.Errorf("%d bytes", len(got))
	}
	if !strings.Contains(got, "140 more not listed.\n") {
		t.Errorf("no count of the plans left out")
	}
}
