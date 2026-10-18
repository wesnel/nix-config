package runs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const session = "46a9d218-3ef8-4e96-978e-9ea630a586fc"

var now = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func TestParseTag(t *testing.T) {
	for s, want := range map[string]Tag{
		"category=triage;repo=acme/widget;pr=12":       {Category: Triage, Repo: "acme/widget", PR: 12},
		"category=review;kind=dispatched;pr=3":         {Category: Review, Kind: Dispatched, PR: 3},
		"category=loop":                                {Category: Loop},
		" category=digest ; kind=standup ":             {Category: Digest, Kind: Standup},
		"plan=2026-10-14-x.box;category=diagram":       {Category: Diagram, Plan: "2026-10-14-x.box"},
		"category=fresh-review;kind=security;repo=a/b": {Category: FreshReview, Kind: Security, Repo: "a/b"},
		// Any repository pr-loop review keeps reviews of.
		"category=review;repo=_a/" + strings.Repeat("b", 120): {Category: Review, Repo: "_a/" + strings.Repeat("b", 120)},
	} {
		got, err := ParseTag(s)
		if err != nil || got != want {
			t.Errorf("ParseTag(%q) = %+v, %v; want %+v", s, got, err, want)
			continue
		}
		if again, err := ParseTag(got.String()); err != nil || again != got {
			t.Errorf("%q does not parse back: %+v, %v", got.String(), again, err)
		}
	}
	for _, s := range []string{
		"", "repo=a/b", "category=chat", "category=loop;category=review", "category=loop;colour=red",
		"category=loop;repo=widget", "category=loop;repo=a/b/c", "category=loop;repo=../x", "category=loop;pr=0",
		"category=loop;pr=-1", "category=loop;pr=1e3", "category=loop;kind=whatever", "category=loop;plan=fix the bug",
		"category=loop;plan=" + strings.Repeat("x", 101), "category=loop;repo=a/b\nkind=demo", "category",
		"category=loop;pr=012", "category=loop;pr=+3", "category=loop;kind=", "category=loop;repo=", "category=",
	} {
		if got, err := ParseTag(s); err == nil {
			t.Errorf("ParseTag(%q) accepted: %+v", s, got)
		}
	}
}

func hookInput(source, id string) []byte {
	return []byte(`{"session_id":"` + id + `","transcript_path":"/x/` + id + `.jsonl","cwd":"/x","hook_event_name":"SessionStart",` +
		`"source":"` + source + `","model":"claude-haiku-5-5"}`)
}

func TestFromSessionStart(t *testing.T) {
	tag := "category=triage;repo=acme/widget;pr=12"
	r, ok, err := FromSessionStart(hookInput("startup", session), tag, now, "box", "/home/u/.claude")
	want := Record{V: 1, RunID: "claude:" + session, Host: "box", Started: now, Provider: "claude", SessionID: session,
		Category: Triage, Labels: Labels{Repo: "acme/widget", PR: 12}, ConfigDir: "/home/u/.claude", Model: "claude-haiku-5-5"}
	if err != nil || !ok || !reflect.DeepEqual(r, want) {
		t.Errorf("startup: %+v, %v, %v; want %+v", r, ok, err, want)
	}
	if r, ok, err := FromSessionStart(hookInput("clear", session), tag, now, "box", ""); err != nil || !ok || r.Started != now {
		t.Errorf("clear: %+v, %v, %v", r, ok, err)
	}
	// A compaction or a resume keeps the session, which is recorded once.
	for _, source := range []string{"compact", "resume"} {
		if _, ok, err := FromSessionStart(hookInput(source, session), tag, now, "box", ""); ok || err != nil {
			t.Errorf("%s recorded: %v, %v", source, ok, err)
		}
	}
	// Another event's input, as a hook listed under the wrong event gets.
	end := []byte(`{"session_id":"` + session + `","hook_event_name":"SessionEnd","reason":"clear"}`)
	for _, tag := range []string{tag, ""} {
		if _, ok, err := FromSessionStart(end, tag, now, "box", ""); ok || err == nil {
			t.Errorf("SessionEnd with tag %q: %v, %v", tag, ok, err)
		}
	}
	// An untagged session is the report's to attribute.
	if _, ok, err := FromSessionStart(hookInput("startup", session), "", now, "box", ""); ok || err != nil {
		t.Errorf("untagged recorded: %v, %v", ok, err)
	}
	for name, c := range map[string]struct {
		input []byte
		tag   string
	}{
		"bad tag":     {hookInput("startup", session), "category=chat"},
		"bad session": {hookInput("startup", "../../x"), tag},
		"no session":  {hookInput("startup", ""), tag},
		"not json":    {[]byte("{"), tag},
	} {
		if r, ok, err := FromSessionStart(c.input, c.tag, now, "box", ""); err == nil || ok {
			t.Errorf("%s: %+v, %v", name, r, ok)
		}
	}
}

// stream is a print session's stream-json output, as Claude Code 2.1.302
// writes it for a session that ran a sub-agent and then a second turn:
// each result carries the process's totals so far.
const stream = `{"type":"system","subtype":"hook_started","session_id":"` + session + `"}
{"type":"system","subtype":"init","cwd":"/x","session_id":"` + session + `","tools":["Task"],"model":"claude-haiku-5-5"}
{"type":"assistant","message":{"content":[{"type":"text","text":"synthetic reply text"}]},"session_id":"` + session + `"}
{"total_cost_usd":0.0067,"session_id":"` + session + `","modelUsage":{"claude-haiku-5-5":{"inputTokens":6,"outputTokens":296,"cacheReadInputTokens":41962,"cacheCreationInputTokens":38295,"thinkingTokens":179,"costUSD":0.0067}},"type":"result","subtype":"success","result":"ok","result_index":0}
{"type":"system","subtype":"init","cwd":"/x","session_id":"` + session + `","model":"claude-haiku-5-5"}
{"total_cost_usd":0.0071,"session_id":"` + session + `","modelUsage":{"claude-haiku-5-5":{"inputTokens":10,"outputTokens":371,"cacheReadInputTokens":72776,"cacheCreationInputTokens":39025,"thinkingTokens":250},"claude-opus-5-5":{"inputTokens":1,"outputTokens":2,"cacheReadInputTokens":3,"cacheCreationInputTokens":4}},"type":"result","subtype":"success","result":"ok","result_index":1}
`

func TestStream(t *testing.T) {
	var s Stream
	// Written in pieces that split lines, as a pipe delivers them.
	for i := 0; i < len(stream); i += 7 {
		if _, err := s.Write([]byte(stream[i:min(i+7, len(stream))])); err != nil {
			t.Fatal(err)
		}
	}
	tag := Tag{Category: Loop, Repo: "acme/widget"}
	r, ok := s.Record(tag, now, "box", "/c")
	want := Record{V: 1, RunID: "claude:" + session, Host: "box", Ended: now, Provider: "claude", SessionID: session,
		Category: Loop, Labels: Labels{Repo: "acme/widget"}, ConfigDir: "/c", Model: "claude-haiku-5-5",
		Tokens: &Tokens{Input: 11, CacheRead: 72779, CacheWrite: 39029, Output: 373}}
	if !ok || !reflect.DeepEqual(r, want) {
		t.Errorf("record %+v (tokens %+v), %v; want %+v (tokens %+v)", r, r.Tokens, ok, want, want.Tokens)
	}

	// A process killed before its first result leaves nothing to record.
	var killed Stream
	killed.Write([]byte(strings.SplitN(stream, "\n", 3)[1] + "\n"))
	if r, ok := killed.Record(tag, now, "box", ""); ok {
		t.Errorf("recorded a run with no result: %+v", r)
	}

	lines := strings.Split(stream, "\n")
	// A line too long to be read is dropped, a result too, and the next
	// one read.
	var long Stream
	long.Write([]byte(lines[1] + "\n"))
	long.Write([]byte(`{"type":"user","x":"` + strings.Repeat("y", maxLine) + "\"}\n"))
	padded := strings.Replace(lines[5], `"result":"ok"`, `"result":"`+strings.Repeat("y", maxLine)+`"`, 1)
	long.Write([]byte(padded + "\n"))
	if r, ok := long.Record(tag, now, "box", ""); ok {
		t.Errorf("read a result longer than the limit: %+v", r)
	}
	long.Write([]byte(lines[3] + "\n"))
	if r, ok := long.Record(tag, now, "box", ""); !ok || r.Tokens.Output != 296 {
		t.Errorf("after a long line: %+v, %v", r, ok)
	}

	// The last result is read without a newline after it.
	var unended Stream
	unended.Write([]byte(lines[1] + "\n" + lines[3]))
	if r, ok := unended.Record(tag, now, "box", ""); !ok || r.Tokens.Output != 296 {
		t.Errorf("a last line with no newline: %+v, %v", r, ok)
	}

	// A result of another session, as a command the session runs may
	// print, is not the run's; nor is one before any init.
	const other = "99999999-9999-4999-8999-999999999999"
	var foreign Stream
	foreign.Write([]byte(lines[3] + "\n" + lines[1] + "\n" + strings.ReplaceAll(lines[5], session, other) + "\n"))
	if r, ok := foreign.Record(tag, now, "box", ""); ok {
		t.Errorf("recorded another session's result: %+v", r)
	}

	// Two models with the same total name the run by the first in order.
	var tie Stream
	tie.Write([]byte(lines[1] + "\n" + `{"type":"result","session_id":"` + session + `","modelUsage":{` +
		`"claude-sonnet-5-5":{"inputTokens":5},"claude-haiku-5-5":{"inputTokens":5}}}` + "\n"))
	if r, ok := tie.Record(tag, now, "box", ""); !ok || r.Model != "claude-haiku-5-5" {
		t.Errorf("tie: %+v, %v", r, ok)
	}
}

func TestLedger(t *testing.T) {
	l := Ledger{Dir: filepath.Join(t.TempDir(), "usage")}
	start, _, err := FromSessionStart(hookInput("startup", session), "category=triage;repo=acme/widget;pr=12", now, "box", "/c")
	if err != nil {
		t.Fatal(err)
	}
	var s Stream
	s.Write([]byte(stream))
	end, _ := s.Record(Tag{Category: Triage, Repo: "acme/widget", PR: 12}, now.Add(time.Hour), "box", "/c")
	// A run started 40 days ago and still going, and one that ended 39
	// days ago.
	other := Record{V: 1, RunID: "claude:other", Provider: "claude", SessionID: "other", Category: Loop, Started: now.Add(-40 * 24 * time.Hour)}
	ended := Record{V: 1, RunID: "claude:ended", Provider: "claude", SessionID: "ended", Category: Loop, Started: now.Add(-40 * 24 * time.Hour)}
	if err := l.Append(other, now.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ended, now.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ended.Started, ended.Ended = time.Time{}, now.Add(-39*24*time.Hour)
	if err := l.Append(ended, now.Add(-39*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(start, now); err != nil {
		t.Fatal(err)
	}
	// The latest end counts, whichever was written first.
	early := end
	early.Ended, early.Tokens = now.Add(30*time.Minute), &Tokens{Output: 1}
	if err := l.Append(early, now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(end, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	early.Ended = now.Add(45 * time.Minute)
	if err := l.Append(early, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]Record{
		"category": {V: 1, RunID: "claude:x", Provider: "claude", SessionID: "x", Category: "chat", Started: now},
		"host":     {V: 1, RunID: "claude:x", Provider: "claude", SessionID: "x", Category: Loop, Started: now, Host: "a host"},
	} {
		if err := l.Append(r, now); err == nil {
			t.Errorf("appended a record with a bad %s", name)
		}
	}

	// A line no launcher writes, and one whose label is not from its set.
	f, err := os.OpenFile(filepath.Join(l.Dir, "runs-2026-10.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("not json\n")
	f.WriteString(`{"v":1,"run_id":"claude:y","provider":"claude","session_id":"y","category":"loop","labels":{"repo":"/home/u/secret"}}` + "\n")
	f.Close()

	runs, unreadable, err := l.Read(now.Add(-7 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if unreadable != 2 {
		t.Errorf("unreadable %d, want 2", unreadable)
	}
	got, ok := runs["claude:"+session]
	want := start
	want.Ended, want.Tokens = end.Ended, end.Tokens
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("merged %+v, want %+v", got, want)
	}
	if _, ok := runs["claude:other"]; !ok || len(runs) != 2 {
		t.Errorf("runs overlapping the period, the one still going included: %+v", runs)
	}
	if runs, _, _ := l.Read(now.Add(-60 * 24 * time.Hour)); len(runs) != 3 {
		t.Errorf("runs since 60 days: %+v", runs)
	}

	// Months that ended more than 90 days ago go at the next append.
	for _, month := range []string{"2026-06", "2026-07"} {
		os.WriteFile(filepath.Join(l.Dir, "runs-"+month+".jsonl"), nil, 0o600)
	}
	if err := l.Append(start, now); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(l.Dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"runs-2026-07.jsonl", "runs-2026-09.jsonl", "runs-2026-10.jsonl"}; !reflect.DeepEqual(names, want) {
		t.Errorf("files %q, want %q", names, want)
	}
}

// An old month that cannot be removed fails no append and keeps no other
// from going; a month's file that is a symbolic link is not written
// through.
func TestLedgerFiles(t *testing.T) {
	l := Ledger{Dir: filepath.Join(t.TempDir(), "usage")}
	start, _, err := FromSessionStart(hookInput("startup", session), "category=loop", now, "box", "")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(l.Dir, "runs-2026-05.jsonl", "x"), 0o700)
	os.WriteFile(filepath.Join(l.Dir, "runs-2026-06.jsonl"), nil, 0o600)
	if err := l.Append(start, now); err != nil {
		t.Errorf("append: %v", err)
	}
	if _, err := os.Stat(filepath.Join(l.Dir, "runs-2026-06.jsonl")); !os.IsNotExist(err) {
		t.Errorf("runs-2026-06.jsonl kept: %v", err)
	}

	target := filepath.Join(t.TempDir(), "target")
	os.WriteFile(target, nil, 0o600)
	os.Symlink(target, filepath.Join(l.Dir, "runs-2026-11.jsonl"))
	if err := l.Append(start, now.AddDate(0, 1, 0)); err == nil {
		t.Error("appended through a symbolic link")
	}
	if data, _ := os.ReadFile(target); len(data) > 0 {
		t.Errorf("the link's target was written: %s", data)
	}

	// Nor is a ledger directory that is a symbolic link.
	elsewhere := t.TempDir()
	linked := Ledger{Dir: filepath.Join(t.TempDir(), "usage")}
	os.Symlink(elsewhere, linked.Dir)
	if err := linked.Append(start, now); err == nil {
		t.Error("appended through a linked ledger directory")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) > 0 {
		t.Errorf("the linked directory was written: %v", entries)
	}
	if _, _, err := linked.Read(now.Add(-time.Hour)); err == nil {
		t.Error("read through a linked ledger directory")
	}
}
