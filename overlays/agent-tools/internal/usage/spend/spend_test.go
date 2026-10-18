package spend

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var until = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

// Secrets and text that stand for content: none may reach a report.
const (
	secret      = "ghp_SyntheticSecret0123456789abcdefABCDEF"
	apiKey      = "sk-ant-api03-SyntheticKey0123456789"
	contentText = "synthetic reply text"
	promptText  = "synthetic prompt text"
	taskWord    = "frobnicator"
)

const (
	sInteractive = "11111111-1111-4111-8111-111111111111"
	sLoop        = "22222222-2222-4222-8222-222222222222"
	sReview      = "33333333-3333-4333-8333-333333333333"
	sScratch     = "44444444-4444-4444-8444-444444444444"
	sNoEntry     = "55555555-5555-4555-8555-555555555555"
	sPrevious    = "66666666-6666-4666-8666-666666666666"
	sOld         = "77777777-7777-4777-8777-777777777777"
	sResumed     = "88888888-8888-4888-8888-888888888888"
	sDashboard   = "99999999-9999-4999-8999-999999999999"
)

// usage is one assistant record's usage, as Claude Code writes it.
type usage struct {
	in, cw5m, cw1h, read, out int64
}

func (u usage) json() map[string]any {
	return map[string]any{
		"input_tokens":                u.in,
		"cache_creation_input_tokens": u.cw5m + u.cw1h,
		"cache_read_input_tokens":     u.read,
		"output_tokens":               u.out,
		"output_tokens_details":       map[string]any{"thinking_tokens": 3},
		"server_tool_use":             map[string]any{"web_search_requests": 0, "web_fetch_requests": 0},
		"service_tier":                "standard",
		"cache_creation":              map[string]any{"ephemeral_1h_input_tokens": u.cw1h, "ephemeral_5m_input_tokens": u.cw5m},
		"speed":                       "standard",
	}
}

// rec is one transcript line.
type rec map[string]any

// assistant is an assistant record of a request, holding one content
// block, as each record of a response does.
func assistant(session, cwd, entry, req string, at time.Time, u usage, stop any, side bool) rec {
	r := rec{
		"parentUuid": "p", "isSidechain": side, "userType": "external", "cwd": cwd, "sessionId": session,
		"version": "2.1.302", "gitBranch": "main", "type": "assistant", "uuid": req + at.Format("150405.000"),
		"timestamp": at.Format(time.RFC3339Nano), "requestId": req,
		"message": map[string]any{
			"model": "claude-opus-5-5", "id": "msg_" + req, "type": "message", "role": "assistant",
			"content":     []any{map[string]any{"type": "text", "text": contentText + " " + secret}},
			"stop_reason": stop, "stop_sequence": nil, "usage": u.json(),
		},
	}
	if entry != "" {
		r["entrypoint"] = entry
	}
	return r
}

func user(session, cwd string, at time.Time) rec {
	return rec{"type": "user", "sessionId": session, "cwd": cwd, "entrypoint": "cli", "timestamp": at.Format(time.RFC3339Nano),
		"message": map[string]any{"role": "user", "content": promptText + " " + secret + " " + apiKey}}
}

// totals are one model's in a cost-state: input, output (thinking
// included), cache creation, cache read and, of the output, thinking.
type totals struct{ in, out, write, read, thinking int64 }

// costStateLine is the line Claude Code 2.1.302 appends to a transcript
// when its session ends or is cleared: the totals since startTime,
// cumulative, with figures that must never reach a report.
func costStateLine(session string, start time.Time, models map[string]totals) rec {
	usage := map[string]any{}
	for model, u := range models {
		usage[model] = map[string]any{"inputTokens": u.in, "outputTokens": u.out, "thinkingTokens": u.thinking,
			"cacheReadInputTokens": u.read, "cacheCreationInputTokens": u.write, "webSearchRequests": 0,
			"costUSD": 987.65}
	}
	return rec{"type": "cost-state", "sessionId": session, "startTime": start.UnixMilli(), "totalCostUSD": 987.65,
		"totalAPIDuration": 1, "totalDuration": 2, "totalLinesAdded": 3, "totalLinesRemoved": 4,
		"hasUnknownModelCost": false, "modelUsage": usage}
}

func writeLines(t *testing.T, path string, recs ...rec) {
	t.Helper()
	var b bytes.Buffer
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(append(line, '\n'))
	}
	write(t, path, b.String())
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	root, repo, worktree, review, scratch string
	opts                                  Options
}

// day is a time in the week before until.
func day(h, m int) time.Time {
	return until.Add(-24 * time.Hour).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute - 12*time.Hour)
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{root: root,
		repo:     filepath.Join(root, "git", "github.com", "acme", "widget"),
		worktree: filepath.Join(root, "worktrees", "widget", "feature"),
		review:   filepath.Join(root, "reviews"),
		scratch:  filepath.Join(root, "scratch"),
	}
	// A checkout, a linked worktree of it and the log of a pr-loop session
	// started there.
	write(t, filepath.Join(f.repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(f.repo, ".git", "worktrees", "feature", "commondir"), "../..\n")
	write(t, filepath.Join(f.worktree, ".git"), "gitdir: "+filepath.Join(f.repo, ".git", "worktrees", "feature")+"\n")
	writeLines(t, filepath.Join(f.repo, ".git", "pr-loop-session.jsonl"),
		rec{"type": "system", "subtype": "init", "session_id": sLoop, "cwd": f.repo, "tools": []any{promptText}},
		rec{"type": "result", "total_cost_usd": 12.44, "session_id": sLoop, "result": contentText})
	write(t, filepath.Join(f.scratch, "note"), "")

	claude := filepath.Join(root, "claude")
	projects := filepath.Join(claude, "projects")
	p := filepath.Join(projects, "-x-acme-widget")

	// An interactive session: one response over two records, a call after
	// 20 idle minutes, and a quick one after it.
	writeLines(t, filepath.Join(p, sInteractive+".jsonl"),
		user(sInteractive, f.repo, day(10, 0)),
		assistant(sInteractive, f.repo, "cli", "req_1", day(10, 0), usage{2, 0, 1000, 5000, 300}, "tool_use", false),
		assistant(sInteractive, f.repo, "cli", "req_1", day(10, 0).Add(time.Second), usage{2, 0, 1000, 5000, 300}, "tool_use", false),
		assistant(sInteractive, f.repo, "cli", "req_2", day(10, 20), usage{1, 0, 4000, 6000, 50}, "end_turn", false),
		// A session's end writes its totals so far; resumed, it ends with
		// larger ones.
		costStateLine(sInteractive, day(10, 0), map[string]totals{"claude-opus-5-5": {50, 1000, 3000, 30000, 40}}),
		assistant(sInteractive, f.repo, "cli", "req_3", day(10, 22), usage{1, 100, 0, 10000, 10}, "end_turn", false),
		costStateLine(sInteractive, day(10, 0), map[string]totals{
			"claude-opus-5-5": {100, 2000, 9000, 60000, 700}, "claude-haiku-5-5": {50, 30, 500, 0, 0}}),
	)
	// Its sub-agents: one whose request has streaming partials alone, in a
	// linked worktree, and one whose description starts with no role word.
	sub := filepath.Join(p, sInteractive, "subagents")
	writeLines(t, filepath.Join(sub, "agent-a1.jsonl"),
		assistant(sInteractive, f.worktree, "cli", "req_4", day(10, 5), usage{3, 200, 0, 7000, 5}, nil, true),
		assistant(sInteractive, f.worktree, "cli", "req_4", day(10, 5).Add(time.Second), usage{3, 200, 0, 7000, 11}, nil, true),
	)
	write(t, filepath.Join(sub, "agent-a1.meta.json"),
		`{"agentType":"general-purpose","description":"Fix the `+taskWord+` `+secret+`","model":"opus","spawnDepth":1,"toolUseId":"toolu_1"}`)
	writeLines(t, filepath.Join(sub, "agent-a2.jsonl"),
		assistant(sInteractive, f.repo, "cli", "req_5", day(10, 10), usage{1, 50, 0, 2000, 40}, "end_turn", true),
	)
	write(t, filepath.Join(sub, "agent-a2.meta.json"), `{"agentType":"general-purpose","description":"`+apiKey+` build it"}`)

	// A session resumed from the interactive one repeats its first request.
	writeLines(t, filepath.Join(p, sResumed+".jsonl"),
		assistant(sInteractive, f.repo, "cli", "req_1", day(10, 0), usage{2, 0, 1000, 5000, 300}, "tool_use", false),
		assistant(sResumed, f.repo, "cli", "req_6", day(12, 0), usage{1, 10, 0, 100, 1}, "end_turn", false),
	)

	// The pr-loop session the log names.
	writeLines(t, filepath.Join(p, sLoop+".jsonl"),
		assistant(sLoop, f.repo, "sdk-cli", "req_7", day(13, 0), usage{5, 0, 3000, 0, 100}, "end_turn", false))

	// A dispatched review, in its review directory.
	gadget := filepath.Join(f.review, "acme", "gadget", "12")
	writeLines(t, filepath.Join(projects, "-x-reviews", sReview+".jsonl"),
		assistant(sReview, gadget, "sdk-cli", "req_8", day(14, 0), usage{5, 0, 2000, 0, 20}, "end_turn", false))

	// A session outside any repository, that a plan names.
	writeLines(t, filepath.Join(projects, "-x-scratch", sScratch+".jsonl"),
		assistant(sScratch, f.scratch, "cli", "req_9", day(15, 0), usage{1, 0, 0, 500, 5}, "end_turn", false))

	// A session with no entrypoint.
	writeLines(t, filepath.Join(projects, "-x-scratch", sNoEntry+".jsonl"),
		assistant(sNoEntry, f.scratch, "", "req_10", day(15, 30), usage{1, 0, 0, 300, 2}, "end_turn", false))

	// The week before.
	writeLines(t, filepath.Join(p, sPrevious+".jsonl"),
		assistant(sPrevious, f.repo, "cli", "req_11", until.Add(-10*24*time.Hour), usage{0, 0, 1000, 0, 0}, "end_turn", false))

	// Older than both weeks, in a file last written then.
	old := filepath.Join(p, sOld+".jsonl")
	writeLines(t, old,
		assistant(sOld, f.repo, "cli", "req_12", until.Add(-30*24*time.Hour), usage{0, 0, 999999, 0, 0}, "end_turn", false))
	if err := os.Chtimes(old, until.Add(-30*24*time.Hour), until.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// The dashboard session's own config directory.
	dash := filepath.Join(root, "dash")
	writeLines(t, filepath.Join(dash, "projects", "-x-session", sDashboard+".jsonl"),
		assistant(sDashboard, f.scratch, "cli", "req_13", day(16, 0), usage{1, 0, 500, 0, 5}, "end_turn", false))

	f.opts = Options{
		Sources:    []Source{{Dir: claude}, {Dir: dash, Category: "dashboard"}},
		ReviewRoot: f.review,
		Plans:      map[string]string{sScratch: "2026-10-14-x.box"},
		Since:      until.Add(-7 * 24 * time.Hour),
		Until:      until,
	}
	return f
}

func row(t *testing.T, r Report, label string) Row {
	t.Helper()
	for _, x := range r.Rows {
		if x.Label == label {
			return x
		}
	}
	t.Fatalf("no row %q in %+v", label, r.Rows)
	return Row{}
}

func collect(t *testing.T, f fixture, by string) Report {
	t.Helper()
	r, err := Collect(f.opts, by)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestByCategory(t *testing.T) {
	f := newFixture(t)
	r := collect(t, f, "category")

	// req_1 once, though written twice and repeated by the resumed
	// session; req_4 at its largest output, marked a lower bound; the
	// plan's session outside any repository is interactive too.
	in := row(t, r, "interactive")
	want := Tokens{Input: 2 + 1 + 1 + 3 + 1 + 1 + 1, CacheWrite5m: 100 + 200 + 50 + 10, CacheWrite1h: 1000 + 4000,
		CacheRead: 5000 + 6000 + 10000 + 7000 + 2000 + 100 + 500, Output: 300 + 50 + 10 + 11 + 40 + 1 + 5}
	if in.Tokens != want || in.Total != want.Total() {
		t.Errorf("interactive tokens %+v (total %d), want %+v", in.Tokens, in.Total, want)
	}
	if in.Requests != 7 || in.Sessions != 3 || in.Agents != 2 {
		t.Errorf("interactive requests %d sessions %d agents %d, want 7, 3, 2", in.Requests, in.Sessions, in.Agents)
	}
	if !in.OutputLowerBound {
		t.Error("interactive output not marked a lower bound")
	}
	// req_2 follows req_1 by 20 minutes, within req_1's one-hour cache.
	if in.PostIdleCacheWrite != 0 {
		t.Errorf("post-idle cache write %d, want 0", in.PostIdleCacheWrite)
	}
	if in.Previous != 1000 {
		t.Errorf("previous %d, want 1000", in.Previous)
	}

	for label, total := range map[string]int64{"loop": 3105, "review": 2025, "dashboard": 506, "unknown": 303} {
		if got := row(t, r, label); got.Total != total {
			t.Errorf("%s total %d, want %d", label, got.Total, total)
		}
	}
	if last := r.Rows[len(r.Rows)-1].Label; last != "unknown" {
		t.Errorf("last row %q, want unknown", last)
	}
	if r.Total.Total != want.Total()+3105+2025+506+303 {
		t.Errorf("total %d", r.Total.Total)
	}
	if got := row(t, r, "loop").Share; got < 7.33 || got > 7.34 {
		t.Errorf("loop share %.2f%%, want 3105 of 42326", got)
	}
	for _, x := range r.Rows {
		if x.Tokens.Total() >= 999999 {
			t.Errorf("%s holds the request older than both periods", x.Label)
		}
	}
}

func TestByRepoRolePlan(t *testing.T) {
	f := newFixture(t)

	repo := collect(t, f, "repo")
	// The sub-agent in the linked worktree counts for the checkout's repo.
	if got := row(t, repo, "acme/widget").Requests; got != 7 {
		t.Errorf("acme/widget requests %d, want 7", got)
	}
	if got := row(t, repo, "acme/gadget").Requests; got != 1 {
		t.Errorf("acme/gadget requests %d, want 1", got)
	}
	if got := row(t, repo, "unknown").Requests; got != 3 {
		t.Errorf("unknown repo requests %d, want 3", got)
	}

	role := collect(t, f, "role")
	for label, n := range map[string]int{"main": 9, "fix": 1, "unclassified": 1} {
		if got := row(t, role, label).Requests; got != n {
			t.Errorf("role %s requests %d, want %d", label, got, n)
		}
	}

	plan := collect(t, f, "plan")
	if got := row(t, plan, "2026-10-14-x.box").Requests; got != 1 {
		t.Errorf("plan requests %d, want 1", got)
	}

	if _, err := Collect(f.opts, "model"); err == nil {
		t.Error("an unknown dimension was accepted")
	}
}

func TestTop(t *testing.T) {
	r := collect(t, newFixture(t), "category")
	if len(r.Top) == 0 || r.Top[0].ID != sInteractive || r.Top[0].Category != "interactive" ||
		r.Top[0].Repo != "acme/widget" || r.Top[0].Agents != 2 {
		t.Errorf("top %+v", r.Top)
	}
}

// Nothing a transcript holds but numbers and the labels derived from it
// reaches a report: not content, descriptions or secrets in either.
func TestNoContent(t *testing.T) {
	f := newFixture(t)
	for _, by := range []string{"category", "plan", "repo", "role"} {
		r := collect(t, f, by)
		var text bytes.Buffer
		r.Text(&text)
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		for name, out := range map[string]string{"text": text.String(), "json": string(data)} {
			for _, bad := range []string{secret, apiKey, "ghp_", "sk-ant", contentText, promptText, taskWord, f.root} {
				if strings.Contains(out, bad) {
					t.Errorf("by %s, %s output holds %q:\n%s", by, name, bad, out)
				}
			}
		}
	}
}

// How many turns a coordinator spends per sub-agent, and how much each
// role writes to the cache again after its cache expired.
func TestCoordinationAndIdle(t *testing.T) {
	f := newFixture(t)
	const sCoord = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	p := filepath.Join(f.root, "claude", "projects", "-x-coord")
	at := func(m int) time.Time { return day(9, m) }
	main := func(req string, m int, u usage) rec {
		return assistant(sCoord, f.repo, "cli", req, at(m), u, "end_turn", false)
	}
	writeLines(t, filepath.Join(p, sCoord+".jsonl"),
		main("c1", 0, usage{1, 100, 0, 0, 1}),
		main("c2", 1, usage{1, 0, 0, 100, 1}),
		main("c3", 3, usage{1, 0, 0, 100, 1}),
		// Seven minutes after a five-minute write: written again.
		main("c4", 10, usage{1, 500, 0, 100, 1}),
		main("c5", 30, usage{1, 0, 10, 100, 1}),
		// Fifteen minutes after a one-hour write: still cached.
		main("c6", 45, usage{1, 0, 20, 100, 1}),
	)
	sub := filepath.Join(p, sCoord, "subagents")
	writeLines(t, filepath.Join(sub, "agent-b1.jsonl"),
		assistant(sCoord, f.repo, "cli", "b1", at(2), usage{1, 40, 0, 0, 1}, "end_turn", true))
	write(t, filepath.Join(sub, "agent-b1.meta.json"), `{"agentType":"general-purpose","description":"Build it"}`)
	writeLines(t, filepath.Join(sub, "agent-b2.jsonl"),
		assistant(sCoord, f.repo, "cli", "b2", at(20), usage{1, 60, 0, 0, 1}, "end_turn", true))
	write(t, filepath.Join(sub, "agent-b2.meta.json"), `{"agentType":"general-purpose","description":"Review it"}`)

	r := collect(t, f, "category")
	// sCoord: c3 to c6 after b1, of two agents; the interactive session:
	// req_2 and req_3 after a1, of two.
	want := Coordination{Sessions: 2, MainRequests: 6, Agents: 4, PerAgent: 1.5}
	if r.Coordination != want {
		t.Errorf("coordination %+v, want %+v", r.Coordination, want)
	}
	roles := map[string]RoleIdle{}
	for _, x := range r.PostIdleByRole {
		roles[x.Role] = x
	}
	if m := roles["main"]; m.PostIdleCacheWrite != 510 || m.CacheWrite == 0 {
		t.Errorf("main %+v, want 510 post-idle", m)
	}
	if b := roles["build"]; b.PostIdleCacheWrite != 0 || b.CacheWrite != 40 {
		t.Errorf("build %+v", b)
	}
	if len(r.PostIdleByRole) == 0 || r.PostIdleByRole[0].Role != "main" {
		t.Errorf("roles in order %+v", r.PostIdleByRole)
	}
}

func TestText(t *testing.T) {
	r := collect(t, newFixture(t), "category")
	var b bytes.Buffer
	r.Text(&b)
	out := b.String()
	for _, want := range []string{"by category", "interactive", "unknown", "≥", "cache write 1h", "cache had expired",
		"Coordinator requests per sub-agent: 1.0", "Post-idle cache writes by role: main 0% of",
		"Session totals", "claude-haiku-5-5", "still running", "scaled to the period", "breakdown covers"} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
	data, _ := json.Marshal(r)
	for name, s := range map[string]string{"text": out, "json": string(data)} {
		if strings.Contains(s, "$") || strings.Contains(s, "USD") || strings.Contains(s, "987") {
			t.Errorf("%s holds a dollar figure:\n%s", name, s)
		}
	}
}
