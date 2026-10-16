package review

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/claims"
	"github.com/wesnel/agent-tools/internal/evidence"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/relay"
)

// auditBody is a pull request description whose verification claims are
// C1 to C3; what lies outside its section is the author's and stays out
// of the prompt.
const auditBody = `Grow the code.

## Verification beyond CI

- go test passes
- nix build works
- lint is clean

## Limits

the author's limits text
`

type auditFixture struct {
	*fixture
	plan                                 plans.Plan
	body                                 string
	test, codex, other, elsewhere, dirty evidence.Record
}

func (a *auditFixture) id(t *testing.T, r evidence.Record) string {
	t.Helper()
	for _, got := range must(evidence.Read([]string{r.Session}, time.Now())) {
		if got.Command == r.Command {
			return recordID(got)
		}
	}
	t.Fatalf("no record of %q", r.Command)
	return ""
}

// newAuditFixture is focusFixture's repository and plan, run by Claude
// Code session s1, whose evidence log holds a test run and a Codex build
// at TIP, a lint at a commit outside the range, a vet at TIP with the
// worktree changed, and session s2's call at TIP.
func newAuditFixture(t *testing.T) *auditFixture {
	t.Helper()
	f, p := focusFixture(t)
	// The plans' config comes from AGENT_TOOLS_CONFIG, which a baked
	// config directory would ignore.
	for k, v := range map[string]string{"XDG_STATE_HOME": t.TempDir(), "CLAUDE_CODE_SESSION_ID": "s1", "CODEX_THREAD_ID": "", "ECA_CHAT_ID": "",
		relay.URLEnv: "", relay.RelayEnv: ""} {
		t.Setenv(k, v)
	}
	tree := run(t, f.repo, "git", "rev-parse", "HEAD^{tree}")
	empty := run(t, f.repo, "git", "mktree")
	side := run(t, f.repo, "git", "commit-tree", "-p", f.base, "-m", "side", empty)
	zero := 0
	now := time.Now()
	a := &auditFixture{fixture: f, plan: p, body: filepath.Join(f.repo, ".git", "body.md"),
		test: evidence.Record{Time: now, Session: "s1", Cwd: f.repo, Head: f.tip, Tree: tree, Command: "go test ./...",
			Exit: &zero, OutputTail: "ok  \tpkg\n"},
		codex: evidence.Record{Time: now, Session: "s1", Cwd: f.repo, Head: f.tip, Tree: tree, Command: "nix build .#x",
			OutputTail: "built\n"},
		other: evidence.Record{Time: now, Session: "s1", Cwd: f.repo, Head: side, Tree: empty, Command: "make lint",
			Exit: &zero},
		dirty: evidence.Record{Time: now, Session: "s1", Cwd: f.repo, Head: f.tip, Tree: empty, Command: "go vet ./...",
			Exit: &zero},
		elsewhere: evidence.Record{Time: now, Session: "s2", Cwd: f.repo, Head: f.tip, Tree: tree, Command: "echo other-session",
			Exit: &zero},
	}
	for _, r := range []evidence.Record{a.test, a.codex, a.other, a.dirty, a.elsewhere} {
		if err := evidence.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(a.body, []byte(auditBody), 0o644)
	return a
}

type auditOutput struct {
	Reviewer   string `json:"reviewer"`
	Range      string `json:"range"`
	Tip        string `json:"tip"`
	ClaimsHash string `json:"claimsHash"`
	Claims     []struct {
		ID      string   `json:"id"`
		Claim   string   `json:"claim"`
		Verdict string   `json:"verdict"`
		Records []string `json:"records"`
		Why     string   `json:"why"`
	} `json:"claims"`
}

func auditRun(t *testing.T, args ...string) (int, auditOutput, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Main(t.Context(), append([]string{"audit"}, args...), strings.NewReader(""), &stdout, &stderr)
	var out auditOutput
	if code != 2 && stdout.Len() > 0 && !strings.HasPrefix(stdout.String(), ClaimsContract) {
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
		}
	}
	if strings.HasPrefix(stdout.String(), ClaimsContract) {
		return code, out, stdout.String() + "\x00" + stderr.String()
	}
	return code, out, stderr.String()
}

// Codex judges each claim against the records of the session's log made
// in the range; a claim resting only on a call whose exit status is
// unknown, or on no record, is unsupported whatever the reply says, and
// the receipt names the claims' hash and those left unsupported.
func TestAuditCodex(t *testing.T) {
	a := newAuditFixture(t)
	reply := map[string]any{"claims": []map[string]any{
		{"id": "C1", "verdict": "supported", "records": []string{a.id(t, a.test)}, "why": "the run passed"},
		{"id": "C2", "verdict": "supported", "records": []string{a.id(t, a.codex)}, "why": "it built"},
		{"id": "C3", "verdict": "supported", "records": []string{}, "why": "trust me"},
	}}
	t.Setenv("FAKE_CODEX", "claims")
	t.Setenv("FAKE_REPLY", string(must(json.Marshal(reply))))
	code, out, stderr := auditRun(t, "--claims", a.body, a.base+"..HEAD")
	if code != 3 || len(out.Claims) != 3 || out.Reviewer != "codex" || out.Tip != a.tip {
		t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
	}
	if !strings.Contains(stderr, "--session") || !strings.Contains(stderr, evidence.Dir()) {
		t.Errorf("the output does not say how to add an earlier session's log:\n%s", stderr)
	}
	want := claims.Hash(claims.Of(auditBody))
	if out.ClaimsHash != want {
		t.Errorf("hash %s, want %s", out.ClaimsHash, want)
	}
	for i, verdict := range []string{"supported", "unsupported", "unsupported"} {
		if c := out.Claims[i]; c.Verdict != verdict || c.ID != []string{"C1", "C2", "C3"}[i] || c.Claim == "" {
			t.Errorf("claim %d: %+v, want %s", i, c, verdict)
		}
	}
	if !strings.Contains(out.Claims[1].Why, "no evidence") || !strings.Contains(out.Claims[2].Why, "no evidence") {
		t.Errorf("why: %q, %q", out.Claims[1].Why, out.Claims[2].Why)
	}
	prompt := string(must(os.ReadFile(filepath.Join(a.log, "prompt"))))
	if !strings.HasPrefix(prompt, ClaimsContract) {
		t.Errorf("the prompt does not start with the auditor's contract")
	}
	for _, in := range []string{"go test passes", "lint is clean", a.id(t, a.test), "go test ./...", "nix build .#x", "go vet ./...",
		"unknown"} {
		if !strings.Contains(prompt, in) {
			t.Errorf("the prompt lacks %q:\n%s", in, prompt)
		}
	}
	for _, out := range []string{"make lint", "other-session", "the author's limits text"} {
		if strings.Contains(prompt, out) {
			t.Errorf("the prompt holds %q:\n%s", out, prompt)
		}
	}
	receipts := eventsOf(t, a.plan, "route-receipt")
	if len(receipts) != 1 || receipts[0].Focus != "claims" || receipts[0].Claims != want || receipts[0].Findings != "C2 C3" ||
		receipts[0].PatchID == "" {
		t.Errorf("receipts %+v", receipts)
	}
	a.checkCleanedUp(t)
	if err := claims.Gate(a.repo, a.tip, auditBody, false); err == nil || !strings.Contains(err.Error(), "C2, C3") {
		t.Errorf("the gate took claims the audit left unsupported: %v", err)
	}

	t.Run("all supported, with another session", func(t *testing.T) {
		body := "## Verification beyond CI\n\n- go test passes\n- the other session echoed\n"
		os.WriteFile(a.body, []byte(body), 0o644)
		t.Setenv("FAKE_REPLY", string(must(json.Marshal(map[string]any{"claims": []map[string]any{
			{"id": "C1", "verdict": "supported", "records": []string{a.id(t, a.test)}, "why": "passed"},
			{"id": "C2", "verdict": "supported", "records": []string{a.id(t, a.elsewhere)}, "why": "echoed"},
		}}))))
		code, out, stderr := auditRun(t, "--claims", a.body, "--session", "s1", "--session", "s2", a.base+"..HEAD")
		if code != 0 || len(out.Claims) != 2 || out.Claims[1].Verdict != "supported" {
			t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
		}
		receipts := eventsOf(t, a.plan, "route-receipt")
		if last := receipts[len(receipts)-1]; last.Findings != "" || last.Claims != out.ClaimsHash {
			t.Errorf("receipt %+v", last)
		}
		if err := claims.Gate(a.repo, a.tip, body, false); err != nil {
			t.Errorf("the gate refused the audited claims: %v", err)
		}
	})
}

// A record's secrets are taken out again before a reviewer sees it,
// whoever wrote the log.
func TestAuditRedacted(t *testing.T) {
	a := newAuditFixture(t)
	token := "ghp_" + strings.Repeat("c", 36)
	raw := `{"time": "` + time.Now().Format(time.RFC3339) + `", "session": "s1", "cwd": "/w", "head": "` + a.tip +
		`", "command": "echo ` + token + `", "exit": 0, "outputTail": "` + token + `\n"}` + "\n"
	f, _ := os.OpenFile(filepath.Join(evidence.Dir(), "s1."+time.Now().UTC().Format("20060102")+".jsonl"),
		os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	f.WriteString(raw)
	f.Close()
	code, _, prompt := auditRun(t, "--print-prompt", "--claims", a.body, a.base+"..HEAD")
	if code != 0 || strings.Contains(prompt, token) || !strings.Contains(prompt, "[REDACTED GitHub token]") {
		t.Errorf("exit %d:\n%s", code, prompt)
	}
}

var auditNonce = regexp.MustCompile(`"issued": "([0-9a-f]+)"`)

// A sub-agent's reply is checked against the prompt issued for the same
// claims: the claims come from the issued event, never the reply.
func TestAuditCheck(t *testing.T) {
	a := newAuditFixture(t)
	issue := func(t *testing.T) string {
		t.Helper()
		code, _, prompt := auditRun(t, "--print-prompt", "--claims", a.body, a.base+"..HEAD")
		m := auditNonce.FindStringSubmatch(prompt)
		if code != 0 || m == nil {
			t.Fatalf("exit %d, no nonce:\n%s", code, prompt)
		}
		return m[1]
	}
	answer := func(t *testing.T, n string) string {
		t.Helper()
		return reply(t, a.fixture, string(must(json.Marshal(map[string]any{"tip": a.tip, "issued": n, "claims": []map[string]any{
			{"id": "C1", "verdict": "supported", "records": []string{a.id(t, a.test)}, "why": "passed"},
			{"id": "C2", "verdict": "supported", "records": []string{a.id(t, a.test)}, "why": "x"},
			{"id": "C3", "verdict": "supported", "records": []string{a.id(t, a.dirty)}, "why": "y"},
		}}))))
	}

	n := issue(t)
	issued := eventsOf(t, a.plan, "review-issued")
	want := claims.Hash(claims.Of(auditBody))
	if len(issued) != 1 || issued[0].Focus != "claims" || issued[0].Claims != want || issued[0].Nonce != n {
		t.Fatalf("issued %+v", issued)
	}
	path := answer(t, n)
	code, _, stderr := freshReview(t, "--check", path, a.base+"..HEAD")
	refused(t, "a claims reply checked as a review", code, stderr)

	code, out, stderr := auditRun(t, "--check", path, "--claims", a.body, a.base+"..HEAD")
	if code != 0 || out.Reviewer != "subagent" || len(out.Claims) != 3 {
		t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
	}
	if r := eventsOf(t, a.plan, "route-receipt"); len(r) != 1 || r[0].Nonce != n || r[0].Claims != want {
		t.Errorf("receipts %+v", r)
	}
	code, _, stderr = auditRun(t, "--check", path, "--claims", a.body, a.base+"..HEAD")
	refused(t, "checked again", code, stderr)

	n = issue(t)
	path = answer(t, n)
	os.WriteFile(a.body, []byte(strings.Replace(auditBody, "lint is clean", "lint is spotless", 1)), 0o644)
	code, _, stderr = auditRun(t, "--check", path, "--claims", a.body, a.base+"..HEAD")
	refused(t, "other claims", code, stderr)
}

// An audit needs claims, a session whose log to read, and a plan for its
// receipt, and is no review loop's: the loop's calls are in no log here.
func TestAuditRefusals(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, a *auditFixture)
	}{
		{"no claims", func(t *testing.T, a *auditFixture) {
			os.WriteFile(a.body, []byte("## Verification beyond CI\n\n## Limits\n\n- x\n"), 0o644)
		}},
		{"no session", func(t *testing.T, a *auditFixture) { t.Setenv("CLAUDE_CODE_SESSION_ID", "") }},
		{"relayed", func(t *testing.T, a *auditFixture) { t.Setenv(relay.RelayEnv, "1") }},
		{"in a sandbox with a relay", func(t *testing.T, a *auditFixture) { t.Setenv(relay.URLEnv, "http://127.0.0.1:1") }},
		{"in a loop session", func(t *testing.T, a *auditFixture) { t.Setenv("PR_LOOP_SESSION", "1") }},
		{"no plan", func(t *testing.T, a *auditFixture) {
			config := filepath.Join(t.TempDir(), "config.json")
			os.WriteFile(config, []byte(`{"plans": {"dir": "`+filepath.Join(t.TempDir(), "agents")+`", "host": "box"}}`), 0o644)
			t.Setenv("AGENT_TOOLS_CONFIG", config)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newAuditFixture(t)
			t.Setenv("FAKE_CODEX", "claims")
			t.Setenv("FAKE_REPLY", `{"claims": []}`)
			c.setup(t, a)
			code, _, stderr := auditRun(t, "--claims", a.body, a.base+"..HEAD")
			refused(t, c.name, code, stderr)
			if _, err := os.Stat(filepath.Join(a.log, "prompt")); err == nil {
				t.Error("Codex ran")
			}
		})
	}
}
