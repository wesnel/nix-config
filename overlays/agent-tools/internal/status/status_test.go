package status

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

// fakeGitHub puts fakeGh on PATH answering with text.
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

// reviewRoot puts the reviews' directory, pr-review under
// $XDG_CACHE_HOME, in a new one.
func reviewRoot(t *testing.T) string {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	return filepath.Join(cache, "pr-review")
}

var now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)

func TestGitHub(t *testing.T) {
	fakeGitHub(t, reply)
	root := reviewRoot(t)
	record := filepath.Join(root, "p", "q", "3", head1[:12]+".json")
	os.MkdirAll(filepath.Dir(record), 0o755)
	os.WriteFile(record, []byte("{}"), 0o644)

	g, stale, err := GitHubAt(context.Background(), filepath.Join(t.TempDir(), "github.json"), now)
	if err != nil {
		t.Fatal(err)
	}
	if g == nil || stale {
		t.Fatalf("GitHub %v, stale %v", g, stale)
	}
	wantMine := []PR{
		{Repo: "o/r", Number: 7, Title: "Add the loop", Draft: true, CI: "failure", Review: "changes-requested", Unresolved: 2},
		{Repo: "o/r", Number: 8, CI: "none", Review: "none"},
	}
	if !reflect.DeepEqual(g.Mine, wantMine) || g.MineTotal != 3 {
		t.Errorf("mine %+v of %d, want %+v of 3", g.Mine, g.MineTotal, wantMine)
	}
	wantRequests := []Request{
		{Repo: "p/q", Number: 3, Title: "Fix the parser", Requested: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC), Head: head1, CI: "failure", Record: "yes"},
		{Repo: "p/q", Number: 4, Draft: true, Team: true, Requested: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC), Head: head2, CI: "none", Record: "no"},
	}
	if !reflect.DeepEqual(g.Requests, wantRequests) || g.RequestsTotal != 2 {
		t.Errorf("requests %+v of %d, want %+v of 2", g.Requests, g.RequestsTotal, wantRequests)
	}
}

// A read within a minute of the last fetch reads the cache, and a failed
// fetch falls back on the last good result, stale.
func TestCache(t *testing.T) {
	f := fakeGitHub(t, reply)
	reviewRoot(t)
	cache := filepath.Join(t.TempDir(), "github.json")
	if _, _, err := GitHubAt(context.Background(), cache, now); err != nil {
		t.Fatal(err)
	}
	g, stale, err := GitHubAt(context.Background(), cache, now.Add(30*time.Second))
	if err != nil || f.calls(t) != 1 || stale || g == nil || !g.At.Equal(now) {
		t.Errorf("within a minute: %d calls, stale %v, GitHub %+v, %v", f.calls(t), stale, g, err)
	}

	t.Setenv("FAKE_GH_FAIL", "1")
	g, stale, err = GitHubAt(context.Background(), cache, now.Add(2*time.Minute))
	if err == nil || f.calls(t) != 2 || !stale || g == nil || !g.At.Equal(now) || len(g.Mine) != 2 {
		t.Errorf("after a failed fetch: %d calls, stale %v, GitHub %+v, %v", f.calls(t), stale, g, err)
	}

	g, _, err = GitHubAt(context.Background(), filepath.Join(t.TempDir(), "github.json"), now)
	if err == nil || g != nil {
		t.Errorf("without a cache: GitHub %+v, %v", g, err)
	}
}

// hostile is text an attacker controls on GitHub, each piece carrying the
// marker pwn.
var hostile = []string{
	"o/r]] * pwn",
	"o/r\n* TODO pwn",
	"pwn\n#+begin_src",
	"[[elisp:(pwn)]]",
	"| pwn |",
	"#+CALL: pwn()",
}

// Of what an attacker writes on GitHub only a title is kept, for an escaped
// page alone: a branch or login is never asked for, and a repository name,
// state or head that is not one is dropped or kept as "unknown".
func TestShapes(t *testing.T) {
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
			g, _, err := GitHubAt(context.Background(), filepath.Join(t.TempDir(), "github.json"), now)
			if err != nil {
				t.Fatal(err)
			}
			if len(g.Mine) != 1 || g.Mine[0].CI != "unknown" || g.Mine[0].Review != "unknown" || g.Mine[0].Title != h {
				t.Errorf("mine: %+v", g.Mine)
			}
			if len(g.Requests) != 1 || g.Requests[0].Number != 5 || g.Requests[0].Title != h || g.Requests[0].CI != "unknown" {
				t.Errorf("requests: %+v", g.Requests)
			}
		})
	}
}
