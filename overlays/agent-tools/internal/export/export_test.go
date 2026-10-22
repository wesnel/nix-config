package export

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

var day = time.Date(2026, 10, 14, 13, 5, 0, 0, time.Local)

// fakeToken has a GitHub token's shape, which redaction knows.
const fakeToken = "ghp_SENTINELaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

const head = "0123456789abcdef0123456789abcdef01234567"

// theirsCommitted is when mac's plan reached the notes repository, in
// mac's zone.
const theirsCommitted = "2026-10-14T20:05:00+02:00"

// reply is GitHub's answer: one pull request of the operator's and one
// review asked of them, with fields the export must drop.
const reply = `{"data": {
  "viewer": {"login": "SENTINEL-login"},
  "mine": {"issueCount": 1, "nodes": [
    {"number": 7, "title": "Add the loop ` + fakeToken + `", "isDraft": true, "reviewDecision": "APPROVED",
     "body": "SENTINEL-body", "headRefName": "SENTINEL-branch", "author": {"login": "SENTINEL-author"},
     "repository": {"nameWithOwner": "o/r"},
     "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "SUCCESS"}}}]},
     "reviewThreads": {"nodes": [{"isResolved": false, "comments": {"nodes": [{"body": "SENTINEL-thread"}]}}]}}
  ]},
  "requested": {"issueCount": 1, "nodes": [
    {"number": 3, "title": "Fix the parser\u202e\u200b", "isDraft": true, "headRefOid": "` + head + `",
     "body": "SENTINEL-request-body", "repository": {"nameWithOwner": "p/q"},
     "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "FAILURE"}}}]},
     "reviewRequests": {"nodes": [{"requestedReviewer": {"__typename": "Team", "name": "SENTINEL-team"}}]},
     "timelineItems": {"nodes": [
       {"createdAt": "2026-10-13T09:00:00Z", "requestedReviewer": {"__typename": "Team", "name": "SENTINEL-team"}}]}}
  ]}
}}`

// fakeGitHub puts a gh on PATH that answers `gh api graphql` with reply,
// or fails where $FAKE_GH_FAIL is set, and puts a review of p/q#3 in the
// reviews' directory under a new $XDG_CACHE_HOME.
func fakeGitHub(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	answer := filepath.Join(t.TempDir(), "reply.json")
	os.WriteFile(answer, []byte(reply), 0o644)
	script := "#!/bin/sh\nif [ \"$1 $2\" != \"api graphql\" ]; then exit 64; fi\n" +
		"if [ -n \"$FAKE_GH_FAIL\" ]; then echo 'fake gh: down' >&2; exit 1; fi\ncat " + answer + "\n"
	os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_FAIL", "")
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	record := filepath.Join(cache, "pr-review", "p", "q", "3", head[:12]+".json")
	os.MkdirAll(filepath.Dir(record), 0o755)
	os.WriteFile(record, []byte(`{"note": "SENTINEL-record"}`), 0o644)
}

// under puts text after the line heading in file.
func under(t *testing.T, file, heading, text string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), heading+"\n", heading+"\n"+text, 1)
	if edited == string(data) {
		t.Fatalf("%s has no %q", file, heading)
	}
	os.WriteFile(file, []byte(edited), 0o644)
}

func write(t *testing.T, file, text string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(file), 0o755)
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// git runs git in dir with env added.
func git(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// fixture is a notes repository, with a remote, holding a plans directory,
// box's, with a plan of box's with every section filled, synced and then
// changed, one of mac's, committed in mac's zone, and a halted one; beside
// it notes and org files, and outside it a plan linked into the directory.
// Every text the export must leave out holds SENTINEL.
func fixture(t *testing.T) (c plans.Config, own, theirs plans.Plan) {
	t.Helper()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "Operator", "GIT_AUTHOR_EMAIL": "operator@example.com",
		"GIT_COMMITTER_NAME": "Operator", "GIT_COMMITTER_EMAIL": "operator@example.com"} {
		t.Setenv(k, v)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	origin, notes := filepath.Join(root, "origin.git"), filepath.Join(root, "notes")
	git(t, root, nil, "init", "-q", "--bare", "-b", "main", origin)
	git(t, root, nil, "init", "-q", "-b", "main", notes)
	git(t, notes, nil, "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "Start")
	git(t, notes, nil, "remote", "add", "origin", origin)
	git(t, notes, nil, "push", "-q", "origin", "main")

	c = plans.Config{Dir: filepath.Join(notes, "agents"), Host: "box"}
	made, err := c.New(plans.NewOptions{Title: "Rotate " + fakeToken + "\u202e", Slug: "loop", Repos: []string{"o/SENTINEL-repo"},
		Branches: []string{"main..SENTINEL-branch"}, Worktrees: []string{"/w/SENTINEL-worktree"},
		Sessions: []string{"claude:SENTINEL-session"}, Harness: "claude",
		Goal: "Run the loop. password=SENTINEL-goal-password"}, day)
	if err != nil {
		t.Fatal(err)
	}
	file := made.Plan
	under(t, file, "** Steps", "*** TODO Write the loop\n:PROPERTIES:\n:ID: S1\n:END:\nSENTINEL-step-body\n"+
		"*** TODO Test the loop\n:PROPERTIES:\n:ID: S2\n:AFTER: S1\n:END:\n")
	under(t, file, "** Distribution", "*** TODO SENTINEL-distribution\n:PROPERTIES:\n:ID: P1\n:END:\n")
	under(t, file, "** Follow-ups", "*** TODO SENTINEL-followup\n:PROPERTIES:\n:ID: F1\n:END:\n")
	under(t, file, "** References", "- [[file:/SENTINEL-reference.org]]\n"+
		"*** TODO SENTINEL-reference-step\n:PROPERTIES:\n:ID: S8\n:END:\n")
	under(t, file, "** Verification", "SENTINEL-verification\n")
	part, err := c.New(plans.NewOptions{Title: "SENTINEL-part-title", Parent: file, Part: "P1",
		Goal: "SENTINEL-part-goal"}, day)
	if err != nil {
		t.Fatal(err)
	}
	under(t, part.File, "** Steps", "*** TODO Part step\n:PROPERTIES:\n:ID: P1.S1\n:END:\n")

	outside := filepath.Join(root, "outside")
	linked := "*** TODO SENTINEL-outside-part\n:PROPERTIES:\n:ID: P2.S1\n:END:\n"
	write(t, filepath.Join(outside, "part.org"), linked)
	os.Symlink(filepath.Join(outside, "part.org"), strings.TrimSuffix(file, ".org")+".P2.org")
	for _, name := range []string{"2026-10-14-outside.box.org", "2026-10-14-outside.box.log.org"} {
		write(t, filepath.Join(outside, name), "#+title: SENTINEL-outside-plan\n* Plan\n:PROPERTIES:\n:ID: 2026-10-14-outside.box\n:STATUS: planning\n:END:\n")
		os.Symlink(filepath.Join(outside, name), filepath.Join(c.Dir, name))
	}

	if own, err = c.Open(file); err != nil {
		t.Fatal(err)
	}
	if _, err := own.Ask(plans.AskOptions{Question: "Keep the flag?", Recommendation: "no, drop it", Priority: "A",
		Context: "Nothing reads it.", Alternatives: []string{"keep it :: costs a flag", "rename it"}}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := own.Ask(plans.AskOptions{Question: "Which name?", Recommendation: "loop"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := own.Decide("Q2", plans.DecideOptions{Answer: "loop, as recommended"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := own.Ask(plans.AskOptions{Question: "SENTINEL-waiting-question", Recommendation: "SENTINEL-waiting",
		After: []string{"S1"}}, day); err != nil {
		t.Fatal(err)
	}
	// Settled in Emacs: no ANSWER, a drawer of the operator's own, then
	// the LOGBOOK note Org writes.
	under(t, file, "** Questions", "*** DECIDED Settled in Emacs?\n:PROPERTIES:\n:ID: Q9\n:END:\n"+
		":PRIVATE:\n  SENTINEL-private-drawer\n:END:\n"+
		":LOGBOOK:\n- State \"DECIDED\"    from \"ASK\"        [2026-10-14 Tue 13:05] \\\\\n  yes, in Emacs\n:END:\n")
	if _, err := own.Log(plans.Event{Kind: "started", Item: "S1", Summary: "SENTINEL-log-summary"}, day); err != nil {
		t.Fatal(err)
	}

	mac := plans.Config{Dir: c.Dir, Host: "mac"}
	made, err = mac.New(plans.NewOptions{Title: "Their plan", Slug: "theirs", Goal: "Their goal."}, day)
	if err != nil {
		t.Fatal(err)
	}
	if theirs, err = c.Open(made.Plan); err != nil {
		t.Fatal(err)
	}
	if _, err := c.New(plans.NewOptions{Title: "SENTINEL-halted", Slug: "halted", Status: "halted"}, day); err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(c.Dir, "notes.org"), "* SENTINEL-plans-dir-note\n")
	write(t, filepath.Join(notes, "notes", "meeting.org"), "* SENTINEL-notes\n")
	for _, name := range []string{"inbox.org", "diary.org", "refile.org"} {
		write(t, filepath.Join(notes, "org", name), "* TODO SENTINEL-org-"+name+"\n")
		write(t, filepath.Join(root, "org", name), "* TODO SENTINEL-org-"+name+"\n")
	}

	// mac's sync, then box's, then a change of box's not yet synced.
	rel := func(p plans.Plan) []string {
		return []string{filepath.Join("agents", filepath.Base(p.File())), filepath.Join("agents", p.Name+".log.org")}
	}
	git(t, notes, nil, append([]string{"add", "--"}, rel(theirs)...)...)
	git(t, notes, []string{"GIT_COMMITTER_DATE=" + theirsCommitted}, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "Sync plans from mac")
	if _, err := c.Sync(plans.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	under(t, file, "** Verification", "SENTINEL-after-sync\n")
	fakeGitHub(t)
	return c, own, theirs
}

// The keys each document may hold, an array's elements' as "name[].key".
var allowed = map[string][]string{
	"plans": {"id", "title", "goal", "status", "host", "updated",
		"steps", "steps[].id", "steps[].order", "steps[].title", "steps[].keyword", "steps[].ready",
		"decisions", "decisions[].id", "decisions[].question", "decisions[].answer",
		"questions", "questions[].id", "questions[].text", "questions[].priority", "questions[].context",
		"questions[].options", "questions[].options[].key", "questions[].options[].label",
		"questions[].options[].why", "questions[].options[].recommended"},
	"github": {"at", "stale", "mineTotal", "requestsTotal",
		"mine", "mine[].repo", "mine[].number", "mine[].title", "mine[].draft", "mine[].ci", "mine[].review",
		"mine[].unresolved",
		"requests", "requests[].repo", "requests[].number", "requests[].title", "requests[].draft",
		"requests[].team", "requests[].requested", "requests[].head", "requests[].ci", "requests[].record"},
	"meta": {"updated", "host", "repo", "pushed", "unsynced", "fetched"},
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// keys adds the path of every key of v under path to all, and to nonZero
// where its value is not the zero of its JSON type.
func keys(v any, path string, all, nonZero map[string]bool) {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			p := join(path, k)
			all[p] = true
			switch c := child.(type) {
			case string:
				nonZero[p] = nonZero[p] || c != ""
			case float64:
				nonZero[p] = nonZero[p] || c != 0
			case bool:
				nonZero[p] = nonZero[p] || c
			case []any:
				nonZero[p] = nonZero[p] || len(c) > 0
			case map[string]any:
				nonZero[p] = nonZero[p] || len(c) > 0
			}
			keys(child, p, all, nonZero)
		}
	case []any:
		for _, child := range v {
			keys(child, path+"[]", all, nonZero)
		}
	}
}

// tagKeys adds the JSON key paths of type t under path, failing on a key
// that could be left out of a document, so that a field only a fixture
// leaves empty cannot hide from the sentinel test.
func tagKeys(t *testing.T, typ reflect.Type, path string, out map[string]bool) {
	t.Helper()
	for i := range typ.NumField() {
		f := typ.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" || opts != "" {
			t.Errorf("%s.%s: JSON tag %q, want a plain name", typ, f.Name, f.Tag.Get("json"))
			continue
		}
		p := join(path, name)
		out[p] = true
		switch ft := f.Type; ft.Kind() {
		case reflect.Slice:
			if ft.Elem().Kind() == reflect.Struct {
				tagKeys(t, ft.Elem(), p+"[]", out)
			} else {
				t.Errorf("%s.%s: a slice of %s", typ, f.Name, ft.Elem())
			}
		case reflect.Struct, reflect.Map, reflect.Interface, reflect.Pointer:
			t.Errorf("%s.%s: %s, which the allowlist cannot pin", typ, f.Name, ft)
		}
	}
}

// Every document type's JSON keys are the allowlist, each always written.
func TestDocumentTypes(t *testing.T) {
	for collection, typ := range map[string]reflect.Type{"plans": reflect.TypeFor[Plan](),
		"github": reflect.TypeFor[GitHub](), "meta": reflect.TypeFor[Sync]()} {
		got := map[string]bool{}
		tagKeys(t, typ, "", got)
		if want := allowed[collection]; !reflect.DeepEqual(slices.Sorted(maps.Keys(got)), slices.Sorted(slices.Values(want))) {
			t.Errorf("%s: keys %v, want %v", collection, slices.Sorted(maps.Keys(got)), want)
		}
	}
}

// What leaves the host is the allowlist and nothing else: every document's
// keys are exactly the allowed ones, each set somewhere in the fixture,
// and no text outside the exported plan fields, nor a secret or a
// formatting character within them, reaches any document.
func TestSentinel(t *testing.T) {
	c, own, theirs := fixture(t)
	out := filepath.Join(t.TempDir(), "out")
	cache := filepath.Join(t.TempDir(), "github.json")
	if _, err := Collect(context.Background(), c, cache, day); err != nil {
		t.Fatal(err)
	}
	// The fetch fails after the cache's minute, so GitHub's part is stale.
	t.Setenv("FAKE_GH_FAIL", "1")
	e, err := Collect(context.Background(), c, cache, day.Add(2*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "fake gh: down") {
		t.Fatalf("a failed fetch: %v", err)
	}
	if err := Write(out, e); err != nil {
		t.Fatal(err)
	}
	var files []string
	got, nonZero := map[string]map[string]bool{}, map[string]map[string]bool{}
	filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(out, path)
		files = append(files, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{"sentinel", "ghp_", "\u202e", "\u200b"} {
			if strings.Contains(strings.ToLower(string(data)), s) {
				t.Errorf("%s holds %q:\n%s", rel, s, data)
			}
		}
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		collection := strings.Split(rel, string(filepath.Separator))[0]
		if got[collection] == nil {
			got[collection], nonZero[collection] = map[string]bool{}, map[string]bool{}
		}
		keys(doc, "", got[collection], nonZero[collection])
		return nil
	})
	for collection, list := range allowed {
		want := slices.Sorted(slices.Values(list))
		if g := slices.Sorted(maps.Keys(got[collection])); !slices.Equal(g, want) {
			t.Errorf("%s: keys %v, want %v", collection, g, want)
		}
		var zero []string
		for _, k := range want {
			if !nonZero[collection][k] {
				zero = append(zero, k)
			}
		}
		if len(zero) > 0 {
			t.Errorf("%s: the fixture never sets %v", collection, zero)
		}
	}
	wantFiles := []string{"github/status.json", "meta/sync.json", "plans/" + own.Name + ".json", "plans/" + theirs.Name + ".json"}
	slices.Sort(files)
	slices.Sort(wantFiles)
	if !slices.Equal(files, wantFiles) {
		t.Errorf("files %q, want %q", files, wantFiles)
	}
}

// The exported values: plan text redacted and stripped of formatting
// characters, steps only from Steps sections with their log's keyword and
// readiness, questions as options with the recommendation first, answers
// from ANSWER or the LOGBOOK alone, times with their zone, and GitHub's
// keys, titles and states.
func TestExport(t *testing.T) {
	c, own, theirs := fixture(t)
	e, err := Collect(context.Background(), c, filepath.Join(t.TempDir(), "github.json"), day)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Plans) != 2 || e.Plans[0].ID != theirs.Name || e.Plans[1].ID != own.Name {
		t.Fatalf("plans %+v", e.Plans)
	}
	got := e.Plans[1]
	want := Plan{ID: own.Name, Title: "Rotate [REDACTED GitHub token]", Goal: "Run the loop. password=[REDACTED]",
		Status: "planning", Host: "box", Updated: day.Format(time.RFC3339),
		Steps: []Step{{ID: "S1", Order: 0, Title: "Write the loop", Keyword: "STARTED", Ready: true},
			{ID: "S2", Order: 1, Title: "Test the loop", Keyword: "TODO"},
			{ID: "P1.S1", Order: 2, Title: "Part step", Keyword: "TODO", Ready: true}},
		Decisions: []Decision{{ID: "Q9", Question: "Settled in Emacs?", Answer: "yes, in Emacs"},
			{ID: "Q2", Question: "Which name?", Answer: "loop, as recommended"}},
		Questions: []Question{{ID: "Q1", Text: "Keep the flag?", Priority: "A", Context: "Nothing reads it.",
			Options: []Option{{Key: "A", Label: "no, drop it", Recommended: true}, {Key: "B", Label: "keep it", Why: "costs a flag"},
				{Key: "C", Label: "rename it"}}}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("own plan\n%+v, want\n%+v", got, want)
	}
	if p := e.Plans[0]; p.Host != "mac" || p.Title != "Their plan" || p.Goal != "Their goal." || len(p.Steps) != 0 ||
		p.Updated != theirsCommitted {
		t.Errorf("their plan %+v", p)
	}
	if s := e.Sync; s.Host != "box" || !s.Repo || !s.Unsynced || s.Pushed == "" || s.Fetched == "" ||
		s.Updated != day.Format(time.RFC3339) {
		t.Errorf("sync %+v", s)
	}
	g := e.GitHub
	wantMine := []PR{{Repo: "o/r", Number: 7, Title: "Add the loop [REDACTED GitHub token]", Draft: true, CI: "success",
		Review: "approved", Unresolved: 1}}
	wantRequests := []Request{{Repo: "p/q", Number: 3, Title: "Fix the parser", Draft: true, Team: true,
		Requested: "2026-10-13T09:00:00Z", Head: head, CI: "failure", Record: "yes"}}
	if g.At != day.Format(time.RFC3339) || g.Stale || !reflect.DeepEqual(g.Mine, wantMine) ||
		!reflect.DeepEqual(g.Requests, wantRequests) || g.MineTotal != 1 || g.RequestsTotal != 1 {
		t.Errorf("github %+v", g)
	}
}

// A plan that cannot be read, or whose ID is longer than a document ID may
// be, is named in the error and never exported, and its document is kept;
// one that is gone, or halted now, loses its document. Where the plans
// directory cannot be listed, no document is removed.
func TestFailures(t *testing.T) {
	fakeGitHub(t)
	c := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
	newPlan := func(slug, status string) plans.Plan {
		t.Helper()
		made, err := c.New(plans.NewOptions{Title: "Plan " + slug, Slug: slug, Status: status}, day)
		if err != nil {
			t.Fatal(err)
		}
		p, err := c.Open(made.Plan)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	kept, gone, halting := newPlan("kept", ""), newPlan("gone", ""), newPlan("halting", "")
	out := t.TempDir()
	e, err := Collect(context.Background(), c, filepath.Join(t.TempDir(), "github.json"), day)
	if err != nil || len(e.Plans) != 3 {
		t.Fatalf("first export: %d plans, %v", len(e.Plans), err)
	}
	if err := Write(out, e); err != nil {
		t.Fatal(err)
	}
	doc := func(p plans.Plan) string { return filepath.Join(out, "plans", p.Name+".json") }

	os.Rename(filepath.Join(c.Dir, kept.Name+".log.org"), filepath.Join(c.Dir, "elsewhere"))
	for _, f := range []string{gone.File(), filepath.Join(c.Dir, gone.Name+".log.org")} {
		os.Remove(f)
	}
	data, _ := os.ReadFile(halting.File())
	os.WriteFile(halting.File(), []byte(strings.Replace(string(data), ":STATUS: planning", ":STATUS: halted", 1)), 0o644)
	long := newPlan(strings.Repeat("a", 200), "")
	e, err = Collect(context.Background(), c, filepath.Join(t.TempDir(), "github.json"), day)
	if err == nil || !strings.Contains(err.Error(), kept.Name) || !strings.Contains(err.Error(), long.Name) {
		t.Errorf("the error does not name the failed plans: %v", err)
	}
	if len(e.Plans) != 0 {
		t.Errorf("exported %+v", e.Plans)
	}
	if err := Write(out, e); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[plans.Plan]bool{kept: true, gone: false, halting: false, long: false} {
		if _, err := os.Stat(doc(p)); (err == nil) != want {
			t.Errorf("%s's document: kept %v, want %v", p.Name, err == nil, want)
		}
	}

	// A plans directory that cannot be listed removes nothing.
	os.WriteFile(doc(gone), []byte("{}"), 0o600)
	os.Chmod(c.Dir, 0)
	defer os.Chmod(c.Dir, 0o755)
	e, err = Collect(context.Background(), c, filepath.Join(t.TempDir(), "github.json"), day)
	if err == nil {
		t.Error("an unreadable plans directory is no error")
	}
	if err := Write(out, e); err != nil {
		t.Fatal(err)
	}
	for _, p := range []plans.Plan{kept, gone} {
		if _, err := os.Stat(doc(p)); err != nil {
			t.Errorf("%s's document was removed: %v", p.Name, err)
		}
	}
}

// Long text is cut to its cap in runes, lists to theirs, and a plan
// filled past every cap with the widest characters stays within maxDoc.
func TestCaps(t *testing.T) {
	c := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
	wide := func(n int) string { return strings.Repeat("😀", n) }
	made, err := c.New(plans.NewOptions{Title: wide(300), Slug: "long", Goal: wide(2500)}, day)
	if err != nil {
		t.Fatal(err)
	}
	var steps strings.Builder
	for i := 1; i <= maxSteps+20; i++ {
		fmt.Fprintf(&steps, "*** TODO %s\n:PROPERTIES:\n:ID: S%d\n:END:\n", wide(300), i)
	}
	under(t, made.Plan, "** Steps", steps.String())
	p, _ := c.Open(made.Plan)
	var alternatives []string
	for range maxOptions + 4 {
		alternatives = append(alternatives, wide(300)+" :: "+wide(700))
	}
	for i := range maxQuestions + 5 {
		if _, err := p.Ask(plans.AskOptions{Question: wide(700), Recommendation: wide(300), Context: wide(2500),
			Alternatives: alternatives}, day); err != nil {
			t.Fatal(err)
		}
		if i < maxDecisions+5 {
			if _, err := p.Ask(plans.AskOptions{Question: wide(700), Recommendation: "x", Answer: wide(1500)}, day); err != nil {
				t.Fatal(err)
			}
		}
	}
	for range maxDecisions + 5 - (maxQuestions + 5) {
		if _, err := p.Ask(plans.AskOptions{Question: wide(700), Recommendation: "x", Answer: wide(1500)}, day); err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := Plans(c, day)
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v, %v", got, err)
	}
	plan := got[0]
	q := plan.Questions[0]
	runes := func(s string) int { return len([]rune(s)) }
	if runes(plan.Title) != 200 || runes(plan.Goal) != 2000 || runes(plan.Steps[0].Title) != 200 ||
		runes(q.Text) != 500 || runes(q.Context) != 2000 || runes(q.Options[0].Label) != 200 ||
		runes(q.Options[1].Why) != 500 || runes(plan.Decisions[0].Question) != 500 || runes(plan.Decisions[0].Answer) != 1000 {
		t.Errorf("not capped: title %d, goal %d, step %d, question %d, context %d, label %d, why %d, decision %d, answer %d",
			runes(plan.Title), runes(plan.Goal), runes(plan.Steps[0].Title), runes(q.Text), runes(q.Context),
			runes(q.Options[0].Label), runes(q.Options[1].Why), runes(plan.Decisions[0].Question), runes(plan.Decisions[0].Answer))
	}
	if len(plan.Steps) != maxSteps || len(plan.Questions) != maxQuestions || len(q.Options) != maxOptions ||
		len(plan.Decisions) != maxDecisions {
		t.Errorf("lists not capped: %d steps, %d questions, %d options, %d decisions",
			len(plan.Steps), len(plan.Questions), len(q.Options), len(plan.Decisions))
	}
	out := t.TempDir()
	if err := Write(out, Export{Plans: got, Listed: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(out, "plans", plan.ID+".json"))
	if err != nil || info.Size() > maxDoc {
		t.Errorf("document of %v bytes, over %d (%v)", info.Size(), maxDoc, err)
	}
}

// Write replaces the plans it wrote before with the ones exported now, and
// leaves every other file in the directory alone; it writes into no
// collection directory that is a link.
func TestWrite(t *testing.T) {
	out := t.TempDir()
	write(t, filepath.Join(out, "plans", "2026-01-01-old.box.json"), "{}")
	write(t, filepath.Join(out, "answers", "x.json"), `{"choice": "A"}`)
	e := Export{Plans: []Plan{{ID: "2026-10-14-new.box", Steps: []Step{}, Decisions: []Decision{}, Questions: []Question{}}},
		Listed: true}
	if err := Write(out, e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "plans", "2026-01-01-old.box.json")); err == nil {
		t.Error("the old plan's document is still there")
	}
	for _, f := range []string{"plans/2026-10-14-new.box.json", "answers/x.json", "github/status.json", "meta/sync.json"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	var doc map[string]any
	data, _ := os.ReadFile(filepath.Join(out, "plans", "2026-10-14-new.box.json"))
	if err := json.Unmarshal(data, &doc); err != nil || doc["id"] != "2026-10-14-new.box" {
		t.Errorf("document %s: %v", data, err)
	}

	// With no plans, into a new directory.
	if err := Write(filepath.Join(t.TempDir(), "out"), Export{Listed: true}); err != nil {
		t.Errorf("with no plans: %v", err)
	}

	for _, collection := range []string{"plans", "github", "meta"} {
		linked, elsewhere := t.TempDir(), t.TempDir()
		os.Symlink(elsewhere, filepath.Join(linked, collection))
		if err := Write(linked, e); err == nil {
			t.Errorf("wrote through %s, a link", collection)
		}
		if entries, _ := os.ReadDir(elsewhere); len(entries) > 0 {
			t.Errorf("wrote %d files through %s, a link", len(entries), collection)
		}
	}
}
