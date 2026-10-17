package spend

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func totalRow(t *testing.T, r Report, label string) TotalRow {
	t.Helper()
	for _, x := range r.Totals {
		if x.Label == label {
			return x
		}
	}
	t.Fatalf("no totals row %q in %+v", label, r.Totals)
	return TotalRow{}
}

func topSession(t *testing.T, r Report, id string) Session {
	t.Helper()
	for _, s := range r.Top {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no top session %s in %+v", id, r.Top)
	return Session{}
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// session writes another session's transcript into the fixture's config
// directory.
func (f fixture) session(t *testing.T, id string, recs ...rec) {
	t.Helper()
	writeLines(t, filepath.Join(f.root, "claude", "projects", "-x-acme-widget", id+".jsonl"), recs...)
}

// A session's last cost-state is its total, sub-agents and models the
// per-message records miss included; a session with none, still running,
// stands in with its per-message sums.
func TestRecordedTotals(t *testing.T) {
	f := newFixture(t)
	r := collect(t, f, "category")

	in := totalRow(t, r, "interactive")
	// The interactive session's last cost-state, and the resumed and
	// scratch sessions' sums.
	want := Totals{Input: 150 + 1 + 1, CacheWrite: 9500 + 10, CacheRead: 60000 + 100 + 500, Output: 2030 + 1 + 5}
	if in.Tokens != want || in.Total != want.Total() || in.Sessions != 3 || in.Live != 2 || in.Short != 0 {
		t.Errorf("interactive totals %+v, want %+v over 3 sessions, 2 live", in, want)
	}
	// The breakdown's share of the recorded totals, over the sessions
	// that have one: 8 of 150 input, 5350 of 9500 cache write, 30000 of
	// 60000 cache read, 411 of 2030 output.
	if c := in.Covered; c == nil || !near(c.Input, 100*8.0/150) || !near(c.CacheWrite, 100*5350.0/9500) ||
		!near(c.CacheRead, 50) || !near(c.Output, 100*411.0/2030) {
		t.Errorf("interactive coverage %+v", in.Covered)
	}
	if loop := totalRow(t, r, "loop"); loop.Total != 3105 || loop.Live != 1 || loop.Covered != nil {
		t.Errorf("loop totals %+v, want its sums, live", loop)
	}
	if r.TotalsAll.Sessions != 7 || r.TotalsAll.Live != 6 || r.TotalsAll.Total != 71680+112+506+3105+2025+303+506 {
		t.Errorf("all totals %+v", r.TotalsAll)
	}
	// The session the week before, with no requests in this one.
	if r.TotalsAll.Previous != 1000 || in.Previous != 1000 {
		t.Errorf("previous %d, interactive %d, want the earlier session's 1000", r.TotalsAll.Previous, in.Previous)
	}

	models := map[string]int64{}
	for _, m := range r.Models {
		models[m.Model] = m.Total
	}
	if models["claude-haiku-5-5"] != 580 || models["claude-opus-5-5"] != 71100+112+506+3105+2025+303+506 {
		t.Errorf("models %+v", r.Models)
	}

	if s := topSession(t, r, sInteractive); s.Recorded != 71680 || s.Live || s.Short {
		t.Errorf("top %+v", s)
	}

	// By role, a cost-state cannot be split among the session's agents.
	if role := collect(t, f, "role"); len(role.Totals) != 0 || role.TotalsAll.Total != r.TotalsAll.Total {
		t.Errorf("by role: %+v, %+v", role.Totals, role.TotalsAll)
	}
}

// A sub-agent's records are the cost-state's when made before the first
// line after it, however late after the session's own last record; its
// own records are by line, whatever their times say.
func TestRecordedTotalsCovered(t *testing.T) {
	f := newFixture(t)
	base := collect(t, f, "category").TotalsAll
	const sEnded, sResumedLater = "18181818-1818-4181-8181-181818181818", "19191919-1919-4191-8191-191919191919"
	agent := func(session, req string, at time.Time, read int64) {
		writeLines(t, filepath.Join(f.root, "claude", "projects", "-x-acme-widget", session, "subagents", "agent-"+req+".jsonl"),
			assistant(session, f.repo, "cli", req, at, usage{0, 0, 0, read, 0}, "end_turn", true))
	}

	// Ended: the sub-agent finished an hour after the session's last own
	// record, before the cost-state; nothing follows it.
	f.session(t, sEnded,
		assistant(sEnded, f.repo, "cli", "req_d1", day(17, 0), usage{0, 0, 0, 1000, 0}, "end_turn", false),
		costStateLine(sEnded, day(17, 0), map[string]totals{"claude-opus-5-5": {0, 0, 0, 5000, 0}}))
	agent(sEnded, "req_d2", day(18, 0), 2000)

	// Resumed at 19:00: a sub-agent's work at 18:00 is the cost-state's, at
	// 20:00 not; an own record after the cost-state line is not, though
	// its time says 16:00.
	f.session(t, sResumedLater,
		assistant(sResumedLater, f.repo, "cli", "req_r1", day(17, 0), usage{0, 0, 0, 1000, 0}, "end_turn", false),
		costStateLine(sResumedLater, day(17, 0), map[string]totals{"claude-opus-5-5": {0, 0, 0, 7000, 0}}),
		user(sResumedLater, f.repo, day(19, 0)),
		assistant(sResumedLater, f.repo, "cli", "req_r2", day(16, 0), usage{0, 0, 0, 300, 0}, "end_turn", false))
	agent(sResumedLater, "req_r3", day(18, 0), 2000)
	agent(sResumedLater, "req_r4", day(20, 0), 400)

	r := collect(t, f, "category")
	if s := topSession(t, r, sEnded); s.Recorded != 5000 || s.Live {
		t.Errorf("ended session %+v, want its cost-state's 5000, not live", s)
	}
	if s := topSession(t, r, sResumedLater); s.Recorded != 7000+300+400 || !s.Live {
		t.Errorf("resumed session %+v, want 7000 and 700 after, live", s)
	}
	if got := r.TotalsAll.Total - base.Total; got != 5000+7700 {
		t.Errorf("the sessions add %d, want 12700", got)
	}
}

// A cost-state with no records before it counts where the session started.
func TestRecordedTotalsNoRecordsBefore(t *testing.T) {
	f := newFixture(t)
	base := collect(t, f, "category").TotalsAll
	const sFirst = "20202020-2020-4202-8202-202020202020"
	f.session(t, sFirst,
		costStateLine(sFirst, day(16, 0), map[string]totals{"claude-opus-5-5": {0, 0, 0, 3000, 0}}),
		assistant(sFirst, f.repo, "cli", "req_f1", day(17, 0), usage{0, 0, 0, 200, 0}, "end_turn", false))
	r := collect(t, f, "category")
	if got := r.TotalsAll.Total - base.Total; got != 3200 {
		t.Errorf("the session adds %d to the period, want its 3000 and the 200 after", got)
	}
	if s := topSession(t, r, sFirst); s.Recorded != 3200 {
		t.Errorf("whole total %+v, want 3200", s)
	}
}

// Records after a session's last cost-state, as a resumed session that
// is still running writes, count on top of it as they are; the
// cost-state is not spread over them.
func TestRecordedTotalsStale(t *testing.T) {
	f := newFixture(t)
	base := collect(t, f, "category").TotalsAll

	const sStale = "14141414-1414-4141-8141-141414141414"
	f.session(t, sStale,
		assistant(sStale, f.repo, "cli", "req_t1", day(17, 0), usage{0, 0, 0, 1000, 0}, "end_turn", false),
		costStateLine(sStale, day(17, 0), map[string]totals{"claude-opus-5-5[1m]": {0, 0, 0, 10000, 0}}),
		assistant(sStale, f.repo, "cli", "req_t2", day(18, 0), usage{0, 0, 0, 4000, 0}, "end_turn", false))
	r := collect(t, f, "category")
	if got := r.TotalsAll.Total - base.Total; got != 14000 || r.TotalsAll.Live != base.Live+1 {
		t.Errorf("the session adds %d (want 10000 and 4000 after), live %d", got, r.TotalsAll.Live)
	}
	if s := topSession(t, r, sStale); s.Recorded != 14000 || !s.Live || s.Short {
		t.Errorf("top %+v", s)
	}
	// Coverage counts what the cost-state holds: 1000 of its 10000, beside
	// the interactive session's 30000 of 60000.
	if c := r.TotalsAll.Covered; c == nil || !near(c.CacheRead, 100*31000.0/70000) {
		t.Errorf("coverage %+v", c)
	}
	models := map[string]int64{}
	for _, m := range r.Models {
		models[m.Model] = m.Total
	}
	if models["claude-opus-5-5[1m]"] != 10000 {
		t.Errorf("models %+v, want the bracketed name kept", r.Models)
	}
}

// A cost-state holding fewer tokens than the records before it is short:
// the records stand in, and it is neither live nor in the coverage. One
// holding exactly as many is not.
func TestRecordedTotalsShort(t *testing.T) {
	f := newFixture(t)
	base := collect(t, f, "category").TotalsAll

	const sShort, sEqual = "13131313-1313-4131-8131-131313131313", "15151515-1515-4151-8151-151515151515"
	f.session(t, sShort,
		assistant(sShort, f.repo, "cli", "req_p1", day(18, 0), usage{0, 0, 0, 5000, 0}, "end_turn", false),
		assistant(sShort, f.repo, "cli", "req_p2", day(18, 30), usage{0, 0, 0, 100, 0}, "end_turn", false),
		costStateLine(sShort, day(17, 0), map[string]totals{"claude-opus-5-5": {0, 0, 0, 120, 0}}))
	f.session(t, sEqual,
		assistant(sEqual, f.repo, "cli", "req_e1", day(18, 0), usage{0, 0, 0, 700, 0}, "end_turn", false),
		costStateLine(sEqual, day(17, 0), map[string]totals{"claude-opus-5-5": {0, 0, 0, 700, 0}}))
	r := collect(t, f, "category")
	if got := r.TotalsAll.Total - base.Total; got != 5100+700 || r.TotalsAll.Short != base.Short+1 ||
		r.TotalsAll.Live != base.Live {
		t.Errorf("the sessions add %d (want 5100 and 700), short %d, live %d", got, r.TotalsAll.Short, r.TotalsAll.Live)
	}
	if s := topSession(t, r, sShort); !s.Short || s.Live || s.Recorded != 5100 {
		t.Errorf("short session %+v", s)
	}
	if s := topSession(t, r, sEqual); s.Short || s.Live || s.Recorded != 700 {
		t.Errorf("equal session %+v", s)
	}
}

// A session straddling the period counts in it with the share of its
// per-message tokens made in it, its old sub-agents' included; requests
// before the previous period or after this one count in neither.
func TestRecordedTotalsStraddle(t *testing.T) {
	f := newFixture(t)
	base := collect(t, f, "category")

	const sStraddle = "12121212-1212-4121-8121-121212121212"
	f.session(t, sStraddle,
		assistant(sStraddle, f.repo, "cli", "req_s0", until.Add(-20*24*time.Hour), usage{0, 2000, 0, 0, 0}, "end_turn", false),
		assistant(sStraddle, f.repo, "cli", "req_s1", until.Add(-10*24*time.Hour), usage{0, 3000, 0, 0, 0}, "end_turn", false),
		assistant(sStraddle, f.repo, "cli", "req_s2", day(17, 0), usage{0, 1000, 0, 0, 0}, "end_turn", false),
		assistant(sStraddle, f.repo, "cli", "req_s3", until.Add(time.Hour), usage{0, 1000, 0, 0, 0}, "end_turn", false),
		costStateLine(sStraddle, until.Add(-20*24*time.Hour), map[string]totals{"claude-opus-5-5": {0, 0, 16000, 0, 0}}))
	// A sub-agent that ended long ago, in a file last written then.
	sub := filepath.Join(f.root, "claude", "projects", "-x-acme-widget", sStraddle, "subagents", "agent-s.jsonl")
	writeLines(t, sub,
		assistant(sStraddle, f.repo, "cli", "req_s4", until.Add(-30*24*time.Hour), usage{0, 1000, 0, 0, 0}, "end_turn", true))
	if err := os.Chtimes(sub, until.Add(-30*24*time.Hour), until.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	r := collect(t, f, "category")
	// 16000 over 2000 + 3000 + 1000 + 1000 + 1000 of records: an eighth in
	// this period, three in the previous one.
	if got := r.TotalsAll.Total - base.TotalsAll.Total; got != 2000 {
		t.Errorf("the straddling session adds %d to the period, want an eighth of 16000", got)
	}
	if got := r.TotalsAll.Previous - base.TotalsAll.Previous; got != 6000 {
		t.Errorf("it adds %d to the previous period, want three eighths of 16000", got)
	}
	if s := topSession(t, r, sStraddle); s.Recorded != 16000 {
		t.Errorf("its whole total %d, want 16000", s.Recorded)
	}
	// The per-message breakdown counts req_s1 the week before and req_s2
	// in this one, and neither req_s0 nor req_s3.
	if got := r.Total.Previous - base.Total.Previous; got != 3000 {
		t.Errorf("breakdown previous grew %d, want 3000", got)
	}
	if got := r.Total.Total - base.Total.Total; got != 1000 {
		t.Errorf("breakdown total grew %d, want 1000", got)
	}
}

// A cost-state spreads over the periods kind by kind, as the records of
// each kind do.
func TestRecordedTotalsByKind(t *testing.T) {
	f := newFixture(t)
	base := collect(t, f, "category").TotalsAll
	const sKinds = "17171717-1717-4171-8171-171717171717"
	f.session(t, sKinds,
		assistant(sKinds, f.repo, "cli", "req_k1", until.Add(-10*24*time.Hour), usage{0, 1000, 0, 0, 0}, "end_turn", false),
		assistant(sKinds, f.repo, "cli", "req_k2", day(17, 0), usage{0, 0, 0, 1000, 0}, "end_turn", false),
		costStateLine(sKinds, until.Add(-10*24*time.Hour), map[string]totals{"claude-opus-5-5": {0, 0, 4000, 6000, 0}}))
	r := collect(t, f, "category").TotalsAll
	now := r.Tokens
	now.CacheRead -= base.Tokens.CacheRead
	now.CacheWrite -= base.Tokens.CacheWrite
	if now.CacheRead != 6000 || now.CacheWrite != 0 || r.Previous-base.Previous != 4000 {
		t.Errorf("this period gained cache read %d and write %d, the previous %d; want 6000, 0 and 4000",
			now.CacheRead, now.CacheWrite, r.Previous-base.Previous)
	}
}

// A cost-state counts only in the transcript of the session it names.
func TestRecordedTotalsOwnFile(t *testing.T) {
	f := newFixture(t)
	const sOther = "16161616-1616-4161-8161-161616161616"
	f.session(t, sOther,
		assistant(sOther, f.repo, "cli", "req_o1", day(18, 0), usage{0, 0, 0, 100, 0}, "end_turn", false),
		costStateLine(sOther, day(18, 0), map[string]totals{"claude-opus-5-5": {0, 0, 0, 300, 0}}),
		costStateLine(sInteractive, day(10, 0), map[string]totals{"claude-opus-5-5": {0, 0, 0, 9999999, 0}}))
	r := collect(t, f, "category")
	if s := topSession(t, r, sInteractive); s.Recorded != 71680 {
		t.Errorf("another transcript's cost-state replaced the session's: %+v", s)
	}
	if s := topSession(t, r, sOther); s.Recorded != 300 {
		t.Errorf("the other session %+v", s)
	}
}
