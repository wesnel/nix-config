package status

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

// fakeGh stands in for `gh api graphql`, logging each call on a line: it
// prints $FAKE_GH_REPLY, or fails where $FAKE_GH_FAIL is set.
const fakeGh = `#!/bin/sh
echo "$1 $2" >> "$FAKE_GH_LOG"
if [ "$1 $2" != "api graphql" ]; then echo "fake gh: unexpected $*" >&2; exit 64; fi
if [ -n "$FAKE_GH_FAIL" ]; then echo "fake gh: rate limited" >&2; exit 1; fi
cat "$FAKE_GH_REPLY"
`

const (
	head1 = "0123456789abcdef0123456789abcdef01234567"
	head2 = "89abcdef0123456789abcdef0123456789abcdef"
)

// reply is GitHub's answer for alice: two of their three open pull
// requests, and p/q#3, asked of them and a team, and p/q#4, of a team only.
const reply = `{"data": {
  "viewer": {"login": "alice"},
  "mine": {"issueCount": 3, "nodes": [
    {"number": 7, "title": "Add the loop", "isDraft": true, "reviewDecision": "CHANGES_REQUESTED",
     "repository": {"nameWithOwner": "o/r"},
     "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "FAILURE"}}}]},
     "reviewThreads": {"nodes": [{"isResolved": false}, {"isResolved": true}, {"isResolved": false}]}},
    {"number": 8, "isDraft": false, "reviewDecision": null,
     "repository": {"nameWithOwner": "o/r"},
     "commits": {"nodes": [{"commit": {"statusCheckRollup": null}}]},
     "reviewThreads": {"nodes": []}}
  ]},
  "requested": {"issueCount": 2, "nodes": [
    {"number": 3, "title": "Fix the parser", "isDraft": false, "headRefOid": "` + head1 + `",
     "repository": {"nameWithOwner": "p/q"},
     "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "FAILURE"}}}]},
     "reviewRequests": {"nodes": [
       {"requestedReviewer": {"__typename": "Team"}},
       {"requestedReviewer": {"__typename": "User", "login": "alice"}}]},
     "timelineItems": {"nodes": [
       {"createdAt": "2026-10-14T09:00:00Z", "requestedReviewer": {"__typename": "User", "login": "alice"}},
       {"createdAt": "2026-10-15T09:00:00Z", "requestedReviewer": {"__typename": "Team"}}]}},
    {"number": 4, "isDraft": true, "headRefOid": "` + head2 + `",
     "repository": {"nameWithOwner": "p/q"},
     "reviewRequests": {"nodes": [{"requestedReviewer": {"__typename": "Team"}}]},
     "timelineItems": {"nodes": [
       {"createdAt": "2026-10-13T09:00:00Z", "requestedReviewer": {"__typename": "Team"}}]}}
  ]}
}}`

type fake struct {
	log, reply string
}

// fakeGitHub puts fakeGh on PATH answering with text, and a review
// directory in pr-loop's config.
func fakeGitHub(t *testing.T, text string) *fake {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGh), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f := &fake{log: filepath.Join(t.TempDir(), "gh.log"), reply: filepath.Join(t.TempDir(), "reply.json")}
	os.WriteFile(f.reply, []byte(text), 0o644)
	t.Setenv("FAKE_GH_LOG", f.log)
	t.Setenv("FAKE_GH_REPLY", f.reply)
	t.Setenv("FAKE_GH_FAIL", "")
	return f
}

func (f *fake) calls(t *testing.T) int {
	t.Helper()
	data, _ := os.ReadFile(f.log)
	return strings.Count(string(data), "\n")
}

// reviewRoot sets pr-loop's review directory to a new one.
func reviewRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(t.TempDir(), "pr-loop.json")
	os.WriteFile(config, []byte(`{"review": {"dir": "`+root+`"}}`), 0o644)
	t.Setenv("PR_LOOP_CONFIG", config)
	return root
}

func emptyPlans(t *testing.T) plans.Config {
	return plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
}

var now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)

func TestGitHub(t *testing.T) {
	fakeGitHub(t, reply)
	root := reviewRoot(t)
	record := filepath.Join(root, "p", "q", "3", head1[:12]+".json")
	os.MkdirAll(filepath.Dir(record), 0o755)
	os.WriteFile(record, []byte("{}"), 0o644)

	s, err := Collect(context.Background(), emptyPlans(t), filepath.Join(t.TempDir(), "github.json"), now)
	if err != nil {
		t.Fatal(err)
	}
	if s.GitHub == nil || s.Stale {
		t.Fatalf("GitHub %v, stale %v", s.GitHub, s.Stale)
	}
	wantMine := []PR{
		{Repo: "o/r", Number: 7, Title: "Add the loop", Draft: true, CI: "failure", Review: "changes-requested", Unresolved: 2},
		{Repo: "o/r", Number: 8, CI: "none", Review: "none"},
	}
	if !reflect.DeepEqual(s.GitHub.Mine, wantMine) || s.GitHub.MineTotal != 3 {
		t.Errorf("mine %+v of %d, want %+v of 3", s.GitHub.Mine, s.GitHub.MineTotal, wantMine)
	}
	wantRequests := []Request{
		{Repo: "p/q", Number: 3, Title: "Fix the parser", Requested: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC), Head: head1, CI: "failure", Record: "yes"},
		{Repo: "p/q", Number: 4, Draft: true, Team: true, Requested: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC), Head: head2, CI: "none", Record: "no"},
	}
	if !reflect.DeepEqual(s.GitHub.Requests, wantRequests) || s.GitHub.RequestsTotal != 2 {
		t.Errorf("requests %+v of %d, want %+v of 2", s.GitHub.Requests, s.GitHub.RequestsTotal, wantRequests)
	}
}

// A collect within a minute of the last fetch reads the cache, and a
// failed fetch falls back on the last good result, stamped stale.
func TestCache(t *testing.T) {
	f := fakeGitHub(t, reply)
	reviewRoot(t)
	cache := filepath.Join(t.TempDir(), "github.json")
	c := emptyPlans(t)
	if _, err := Collect(context.Background(), c, cache, now); err != nil {
		t.Fatal(err)
	}
	s, err := Collect(context.Background(), c, cache, now.Add(30*time.Second))
	if err != nil || f.calls(t) != 1 || s.Stale || s.GitHub == nil || !s.GitHub.At.Equal(now) {
		t.Errorf("within a minute: %d calls, stale %v, GitHub %+v, %v", f.calls(t), s.Stale, s.GitHub, err)
	}

	t.Setenv("FAKE_GH_FAIL", "1")
	s, err = Collect(context.Background(), c, cache, now.Add(2*time.Minute))
	if err == nil || f.calls(t) != 2 || !s.Stale || s.GitHub == nil || !s.GitHub.At.Equal(now) || len(s.GitHub.Mine) != 2 {
		t.Errorf("after a failed fetch: %d calls, stale %v, GitHub %+v, %v", f.calls(t), s.Stale, s.GitHub, err)
	}

	s, err = Collect(context.Background(), c, filepath.Join(t.TempDir(), "github.json"), now)
	if err == nil || s.GitHub != nil {
		t.Errorf("without a cache: GitHub %+v, %v", s.GitHub, err)
	}
}

func TestPlans(t *testing.T) {
	c := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
	newPlan := func(slug, state string) plans.Plan {
		t.Helper()
		made, err := c.New(plans.NewOptions{Title: "Add the loop", Slug: slug, Repos: []string{"o/r"}, Status: state}, now)
		if err != nil {
			t.Fatal(err)
		}
		p, err := c.Open(made.Plan)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	working := newPlan("working", "working")
	data, _ := os.ReadFile(working.File())
	steps := "** Steps\n*** TODO One\n:PROPERTIES:\n:ID: S1\n:END:\n*** TODO Two\n:PROPERTIES:\n:ID: S2\n:END:\n"
	os.WriteFile(working.File(), []byte(strings.Replace(string(data), "** Steps\n", steps, 1)), 0o644)
	if _, err := working.Ask(plans.AskOptions{Question: "Which?", Recommendation: "This."}, now); err != nil {
		t.Fatal(err)
	}
	if err := working.SetProperty("", "REVIEWS", []string{"security"}, now); err != nil {
		t.Fatal(err)
	}
	newPlan("halted", "halted")
	newPlan("handed-back", "handed-back")

	got, err := Plans(c, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []Plan{{Name: working.Name, File: working.File(), State: "working", Ready: 2, Questions: 1, Owed: []string{"security"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestOrg(t *testing.T) {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)
	s := Status{At: at, Stale: true, GitHub: &GitHub{
		At:        at.Add(-time.Hour),
		Mine:      []PR{{Repo: "o/r", Number: 7, Title: "Add the loop", Draft: true, CI: "failure", Review: "changes-requested", Unresolved: 2}},
		MineTotal: 3,
		Requests: []Request{
			{Repo: "p/q", Number: 3, Title: "Fix the parser", Requested: time.Date(2026, 10, 14, 9, 30, 0, 0, time.Local), Head: head1, CI: "failure", Record: "yes"},
			{Repo: "p/q", Number: 4, Team: true, Head: head2, CI: "none", Record: "unknown"},
		},
		RequestsTotal: 2,
	}, Plans: []Plan{{Name: "2026-10-16-loop.box", File: "/n/agents/2026-10-16-loop.box.org", State: "working", Ready: 2, Questions: 1, Owed: []string{"security"}}}}
	want := `#+title: Agent status
# Written by plan-dash collect, which replaces it on each run.

Collected [2026-10-16 Fri 12:00]. GitHub as of [2026-10-16 Fri 11:00], stale: the last fetch failed.

* My pull requests
| Pull request | Draft | CI | Review | Unresolved |
|-
| [[https://github.com/o/r/pull/7][o/r#7]] | draft | failure | changes-requested | 2 |
1 of 3 shown.

* Review requests
| Pull request | For | Requested | Head | CI | Record |
|-
| [[https://github.com/p/q/pull/3][p/q#3]] | me | [2026-10-14 Wed 09:30] | 0123456789ab | failure | yes |
| [[https://github.com/p/q/pull/4][p/q#4]] | team |  | 89abcdef0123 | none | unknown |

* Plans
| Plan | Status | Ready | Questions | Routes owed |
|-
| [[file:/n/agents/2026-10-16-loop.box.org][2026-10-16-loop.box]] | working | 2 | 1 | security |
`
	if got := s.Org(); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}

	s = Status{At: at}
	if got := s.Org(); !strings.Contains(got, "GitHub: no data, the fetch failed.") || strings.Contains(got, "* My pull requests") {
		t.Errorf("without GitHub:\n%s", got)
	}
}

// hostile is text an attacker controls on GitHub or in a plan, each piece
// carrying the marker pwn.
var hostile = []string{
	"o/r]] * pwn",
	"o/r\n* TODO pwn",
	"pwn\n#+begin_src",
	"[[elisp:(pwn)]]",
	"| pwn |",
	"#+CALL: pwn()",
}

// Nothing an attacker writes on GitHub, nor in a plan's file, reaches
// status.org: not a title, which is kept for the page alone, nor a branch or
// login, which are never asked for, nor a repository name, state or head
// that is not one.
func TestOrgInjection(t *testing.T) {
	for i, h := range hostile {
		// Numbered, as a test's name is part of its temporary directory's.
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			q := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "\n", `\n`) }
			text := `{"data": {
  "viewer": {"login": "` + q(h) + `"},
  "mine": {"issueCount": 2, "nodes": [
    {"number": 1, "title": "` + q(h) + `", "headRefName": "` + q(h) + `", "isDraft": false,
     "reviewDecision": "` + q(h) + `", "repository": {"nameWithOwner": "o/r"},
     "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "` + q(h) + `"}}}]},
     "reviewThreads": {"nodes": []}},
    {"number": 2, "isDraft": false, "repository": {"nameWithOwner": "` + q(h) + `"},
     "commits": {"nodes": []}, "reviewThreads": {"nodes": []}}
  ]},
  "requested": {"issueCount": 3, "nodes": [
    {"number": 3, "title": "` + q(h) + `", "headRefOid": "` + q(h) + `",
     "repository": {"nameWithOwner": "p/q"},
     "author": {"login": "` + q(h) + `"},
     "reviewRequests": {"nodes": [{"requestedReviewer": {"__typename": "User", "login": "` + q(h) + `"}}]},
     "timelineItems": {"nodes": []}},
    {"number": 4, "headRefOid": "` + head1 + `", "repository": {"nameWithOwner": "p/` + q(h) + `"},
     "reviewRequests": {"nodes": []}, "timelineItems": {"nodes": []}},
    {"number": 5, "title": "` + q(h) + `", "headRefOid": "` + head1 + `", "repository": {"nameWithOwner": "p/q"},
     "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "` + q(h) + `"}}}]},
     "reviewRequests": {"nodes": []}, "timelineItems": {"nodes": []}}
  ]}
}}`
			fakeGitHub(t, text)
			reviewRoot(t)
			dir := filepath.Join(t.TempDir(), "agents")
			c := plans.Config{Dir: dir, Host: "box"}
			made, err := c.New(plans.NewOptions{Title: strings.ReplaceAll(h, "\n", " "), Slug: "loop", Repos: []string{"o/r"}, Status: "working"}, now)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(made.Plan)
			os.WriteFile(made.Plan, []byte(strings.Replace(string(data), ":STATUS: working", ":STATUS: "+strings.ReplaceAll(h, "\n", " "), 1)), 0o644)

			s, err := Collect(context.Background(), c, filepath.Join(t.TempDir(), "github.json"), now)
			if err != nil {
				t.Fatal(err)
			}
			org := s.Org()
			if strings.Contains(org, "pwn") || strings.Contains(org, "elisp") {
				t.Errorf("hostile text in status.org:\n%s", org)
			}
			if s.GitHub == nil {
				t.Fatal("no GitHub result")
			}
			// The title is kept, for the page alone.
			if len(s.GitHub.Mine) != 1 || s.GitHub.Mine[0].CI != "unknown" || s.GitHub.Mine[0].Review != "unknown" || s.GitHub.Mine[0].Title != h {
				t.Errorf("mine: %+v", s.GitHub.Mine)
			}
			if len(s.GitHub.Requests) != 1 || s.GitHub.Requests[0].Number != 5 || s.GitHub.Requests[0].Title != h || s.GitHub.Requests[0].CI != "unknown" {
				t.Errorf("requests: %+v", s.GitHub.Requests)
			}
			assertShape(t, org)

			// The cache, read back, is checked again as it is written.
			s = Status{At: now, GitHub: &GitHub{At: now,
				Mine: []PR{{Repo: h, Number: 1}, {Repo: "o/r", Number: 2, Title: h, CI: h, Review: h}},
				Requests: []Request{{Repo: h, Number: 3, Head: head1}, {Repo: "p/q", Number: 4, Head: h},
					{Repo: "p/q", Number: 5, Title: h, Head: head1, CI: h, Record: h}}},
				Plans: []Plan{{Name: h, File: "/n/x.org"}, {Name: "2026-10-16-loop.box", File: "/n/" + h, State: h, Owed: []string{h}}}}
			org = s.Org()
			if strings.Contains(org, "pwn") || strings.Contains(org, "elisp") {
				t.Errorf("hostile text from the cache in status.org:\n%s", org)
			}
			assertShape(t, org)
		})
	}
}

// Org markup a status.org may hold: its own keywords and headings, and table
// rows whose cells hold keys, states, counts and the links built from them.
var (
	headings = map[string]bool{"* My pull requests": true, "* Review requests": true, "* Plans": true}
	cell     = regexp.MustCompile(`^(\[\[(https://github\.com|file:)[A-Za-z0-9_./-]+\]\[[A-Za-z0-9_./#-]+\]\]|\[[0-9]{4}-[0-9]{2}-[0-9]{2} [A-Z][a-z]{2} [0-9]{2}:[0-9]{2}\]|[A-Za-z0-9 _.-]*)$`)
)

func assertShape(t *testing.T, org string) {
	t.Helper()
	for _, line := range strings.Split(org, "\n") {
		switch {
		case strings.HasPrefix(line, "*"):
			if !headings[line] {
				t.Errorf("heading %q", line)
			}
		case strings.HasPrefix(line, "#"):
			if line != "#+title: Agent status" && line != "# Written by plan-dash collect, which replaces it on each run." {
				t.Errorf("keyword %q", line)
			}
		case strings.HasPrefix(line, "|-"):
		case strings.HasPrefix(line, "|"):
			cells := strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|"), "|")
			for _, c := range cells {
				if !cell.MatchString(strings.TrimSpace(c)) {
					t.Errorf("cell %q in %q", c, line)
				}
			}
		case strings.Contains(line, "[[") || strings.Contains(line, "]]"):
			t.Errorf("link outside a table: %q", line)
		}
	}
}

func TestWrite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plan-dash", "status.org")
	for _, text := range []string{"one\n", "two\n"} {
		if err := Write(file, text); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(file); string(data) != "two\n" || info.Mode().Perm() != 0o444 {
		t.Errorf("%q, mode %v", data, info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Dir(file)); len(entries) != 1 {
		t.Errorf("left %d files", len(entries))
	}
}
