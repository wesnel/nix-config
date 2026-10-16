package review

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/claims"
	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/evidence"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/redact"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/relay"
)

// AuditUsage is `git fresh-review audit -h`.
const AuditUsage = `usage: git fresh-review audit --claims FILE [--session ID]... [--reviewer auto|codex|subagent]
                             [--model M] [--timeout S] BASE..TIP
       git fresh-review audit --print-prompt --claims FILE [--session ID]... BASE..TIP
       git fresh-review audit --check REPLY --claims FILE [--session ID]... BASE..TIP

Audits the verification claims in FILE, a pull request's description or a
sub-agent's report, against this host's evidence log: the shell calls the
PostToolUse hook recorded (pr-loop guard --post), never what an agent says
it ran. The claims are the items and paragraphs under FILE's "## Verification
beyond CI" heading, or, without one, all of FILE, numbered C1 on; their hash
is the claims' alone, whatever else FILE says.

The log read is that of each --session, by default the session of the
harness running this (CLAUDE_CODE_SESSION_ID, CODEX_THREAD_ID or
ECA_CHAT_ID); a Claude Code sub-agent's calls are in its parent's. A
session's logs are named ID.DAY.jsonl in $XDG_STATE_HOME/agent-tools/evidence,
so a claim resting on an earlier session's calls, such as one before /clear,
needs --session for that session and again for this one. Of them,
the records made at a commit of BASE..TIP, at BASE, or on a commit built on
TIP, the newest 300, go to a fresh reviewer, redacted, with the claims and
nothing else of the author's. The reviewer runs as a review does (git
fresh-review -h): Codex, confined, in a throwaway checkout of TIP, or a
sub-agent given the prompt --print-prompt prints, whose reply --check
checks against the prompt issued for the same claims and change.

A claim is supported only where records the reviewer cites show it. One
that cites none, or rests only on records whose exit status is unknown
(Codex's calls, and backgrounded commands, whose end no hook sees), is
unsupported (no evidence), whatever the reply says.

The audit logs a route receipt with focus claims in the plan its commits
are logged in, as a review does, holding the claims' hash, the patch ID of
the change and the claims left unsupported. pr-loop open, and the guard on
gh pr create and gh pr edit --body-file, let a description with the
section through only where the latest such receipt for its claims as they
stand, whose range still has the patch ID it recorded at the branch's tip,
left none unsupported. An audit needs that plan, and is not served to a
review loop: its calls are in no log on this host.

Writes JSON on stdout: reviewer, model, range, tip, claimsHash, sessions and
claims (each with id, claim, verdict, records and why). Exit status: 0
every claim supported; 3 a claim unsupported; 4 Codex cannot review here,
for the reasons a review exits 4; 1 the audit could not run; 2 a usage
error or refusal.
`

// ClaimsContract is the auditor's instructions.
//
//go:embed claims.md
var ClaimsContract string

// claimsFocus is the focus of a claims audit's issued prompt and receipt,
// which the gates look for.
const claimsFocus = plans.ClaimsFocus

// auditSchema is the JSON Schema of an auditor's reply, in the strict
// subset Codex's --output-schema takes.
const auditSchema = `{"type": "object", "additionalProperties": false, "required": ["claims"], "properties": {
 "claims": {"type": "array", "items": {"type": "object", "additionalProperties": false,
  "required": ["id", "verdict", "records", "why"], "properties": {
   "id": {"type": "string"}, "verdict": {"type": "string", "enum": ["supported", "unsupported"]},
   "records": {"type": "array", "items": {"type": "string"}}, "why": {"type": "string"}}}}}}`

// maxRecords is how many of the newest records an audit's prompt holds.
const maxRecords = 300

// AuditedClaim is one claim and the reviewer's verdict on it.
type AuditedClaim struct {
	ID      string   `json:"id"`
	Claim   string   `json:"claim"`
	Verdict string   `json:"verdict"`
	Records []string `json:"records"`
	Why     string   `json:"why"`
}

// AuditOutput is what `git fresh-review audit` prints.
type AuditOutput struct {
	Reviewer   string         `json:"reviewer"`
	Model      *string        `json:"model"`
	Range      string         `json:"range"`
	Tip        string         `json:"tip"`
	ClaimsHash string         `json:"claimsHash"`
	Sessions   []string       `json:"sessions"`
	Claims     []AuditedClaim `json:"claims"`
}

func (o *AuditOutput) unsupported() []string {
	var out []string
	for _, c := range o.Claims {
		if c.Verdict != "supported" {
			out = append(out, c.ID)
		}
	}
	return out
}

// shown is a record as an audit shows it: its ID and where it ran,
// relative to the range.
type shown struct {
	evidence.Record
	id, at string
}

// recordID names a record by its content, so that a record keeps its ID
// however many are logged after it.
func recordID(r evidence.Record) string {
	data, _ := json.Marshal(r)
	sum := sha256.Sum256(data)
	return "R" + hex.EncodeToString(sum[:5])
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, " ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// audit runs `git fresh-review audit` with args and returns its exit
// status.
func audit(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(name+" audit", flag.ContinueOnError)
	claimsFile := flags.String("claims", "", "")
	reviewer := flags.String("reviewer", "auto", "")
	model := flags.String("model", "", "")
	timeout := flags.Int("timeout", 1800, "")
	printPrompt := flags.Bool("print-prompt", false, "")
	check := flags.String("check", "", "")
	var sessions stringList
	flags.Var(&sessions, "session", "")
	var positional []string
	for {
		if code, ok := cli.Parse(flags, args, AuditUsage, stdout, stderr); !ok {
			return code
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		args = flags.Args()[1:]
	}
	report := func(result any, err error, code int) int {
		return cli.Report(name, result, err, code, stdout, stderr)
	}
	if os.Getenv(relay.RelayEnv) == "1" || os.Getenv(relay.URLEnv) != "" || os.Getenv("PR_LOOP_SESSION") != "" {
		return report(nil, refusal.New("a claims audit reads this host's evidence log, which holds no review loop's calls; "+
			"leave verification claims out of what the loop writes, and put them in your report for the operator"), 0)
	}
	if len(positional) != 1 || (*reviewer != "auto" && *reviewer != "codex" && *reviewer != "subagent") ||
		(*check != "" && *printPrompt) {
		fmt.Fprint(stderr, AuditUsage)
		return 2
	}
	subagent := *reviewer == "subagent" && *check == ""
	*printPrompt = *printPrompt || subagent
	if *claimsFile == "" {
		return report(nil, refusal.New("an audit judges claims, and none were given; pass the pull request's description "+
			"or the report as `--claims FILE`"), 0)
	}
	text, err := readFile(*claimsFile, stdin)
	if err != nil {
		return report(nil, err, 0)
	}
	list := claims.Of(text)
	if len(list) == 0 {
		return report(nil, refusal.New("%s holds no claims to audit. Write each as an item under `## %s`, "+
			"or leave the section out", *claimsFile, claims.Heading), 0)
	}
	hash := claims.Hash(list)
	if len(sessions) == 0 {
		if s := plans.EnvSession(); s != "" {
			sessions = append(sessions, s)
		}
	}
	if len(sessions) == 0 {
		return report(nil, refusal.New("an audit reads the evidence log of the sessions that ran the commands, and no "+
			"harness session is set here; pass `--session ID` for each, ID naming its logs ID.DAY.jsonl in %s",
			evidence.Dir()), 0)
	}
	if err := git.CheckConfig(""); err != nil {
		return report(nil, err, 0)
	}
	r, err := resolve("", positional[0])
	if err != nil {
		return report(nil, err, 0)
	}
	session := os.Getenv("CLAUDE_CODE_SESSION_ID")
	c, err := plans.Load()
	if err != nil {
		return report(nil, err, 0)
	}
	if _, _, ok := c.LocateCheckout(session, r.top); !ok {
		return report(nil, refusal.New("an audit's receipt goes in the plan its commits are logged in, and no plan names "+
			"this session, worktree or branch. Start one with `agent-plan new`, as the plan-work skill says, then audit again"), 0)
	}
	records, err := evidence.Read(sessions, time.Now())
	if err != nil {
		return report(nil, err, 0)
	}

	if *check != "" {
		reply, err := readFile(*check, stdin)
		if err != nil {
			return report(nil, err, 0)
		}
		current := r.tip
		tip, err := promptTip(reply, r)
		if err != nil {
			return report(nil, err, 0)
		}
		if tip != "" {
			r.tip = tip
		}
		issued, err := issuedFor(reply, r, current)
		if err != nil {
			return report(nil, err, 0)
		}
		again := fmt.Sprintf("print the prompt with `git fresh-review audit --print-prompt --claims %s %s`, give it to a "+
			"new reviewer, and check that reply", *claimsFile, r.given)
		switch {
		case issued.Nonce == "":
			return report(nil, refusal.New("the reply names no audit prompt as issued; %s", again), 0)
		case issued.Focus != claimsFocus:
			return report(nil, refusal.New("the prompt %s was printed for a review, not an audit; %s", issued.Nonce, again), 0)
		case issued.Claims != hash:
			return report(nil, refusal.New("the prompt %s was printed for other claims than %s holds now; %s",
				issued.Nonce, *claimsFile, again), 0)
		}
		out, err := judge(reply, list, selectRecords(r, records))
		if err != nil {
			return report(nil, err, 0)
		}
		out.Reviewer = "subagent"
		return finishAudit(r, out, hash, sessions, issued.Nonce, report, stderr)
	}

	shown := selectRecords(r, records)
	if *printPrompt {
		nonce, err := issue(r, claimsFocus, hash)
		if err != nil {
			return report(nil, err, 0)
		}
		where := fmt.Sprintf("Read the change in the repository at %s: `git diff %s %s`, `git log -p %s..%s`, and "+
			"`git show %s:PATH` for a file as it is at TIP. Put `\"tip\": \"%s\"` and `\"issued\": \"%s\"` in the JSON "+
			"object you reply with, beside claims.", r.top, r.base, r.tip, r.base, r.tip, r.tip, r.tip, nonce)
		fmt.Fprint(stdout, auditPrompt(r, list, shown, len(records), sessions, where))
		if subagent {
			fmt.Fprintf(stderr, "%s: give this prompt to a new `reviewer` sub-agent, save its reply, and check it with "+
				"`git fresh-review audit --check FILE --claims %s %s`.\n", name, *claimsFile, r.given)
		}
		return 0
	}
	if why := unavailable(*reviewer == "auto"); why != "" {
		fmt.Fprintf(stderr, "%s: Codex cannot audit here: %s. Audit with a fresh `reviewer` sub-agent instead: give it "+
			"the prompt `git fresh-review audit --print-prompt --claims %s %s` prints, save its reply, and check it with "+
			"`git fresh-review audit --check FILE --claims %s %s`.\n", name, why, *claimsFile, r.given, *claimsFile, r.given)
		return 4
	}
	var out *AuditOutput
	err = inCheckout(r, func(wt string) error {
		where := "The working directory is a checkout of TIP holding nothing but the commits; `git diff " + r.base + " " +
			r.tip + "` shows the change."
		reply, err := CodexRun(ctx, Codex{Checkout: wt, Prompt: auditPrompt(r, list, shown, len(records), sessions, where),
			Schema: []byte(auditSchema), Model: *model, Timeout: time.Duration(*timeout) * time.Second, Log: stderr})
		if err != nil {
			return err
		}
		if out, err = judge(reply.Text, list, shown); err != nil {
			return err
		}
		out.Reviewer = "codex"
		if reply.Model != "" {
			out.Model = &reply.Model
		}
		return nil
	})
	var unconfined Unconfined
	if errors.As(err, &unconfined) {
		fmt.Fprintf(stderr, "%s: Codex cannot audit here: %s. Audit with a fresh `reviewer` sub-agent instead, as "+
			"`git fresh-review audit --reviewer subagent --claims %s %s` says.\n", name, unconfined, *claimsFile, r.given)
		return 4
	}
	if err != nil {
		return report(nil, err, 0)
	}
	return finishAudit(r, out, hash, sessions, "", report, stderr)
}

// selectRecords are the records made at a commit of r, at its base, or on
// a commit built on its tip, each with where it ran, the newest maxRecords.
// A record at a commit a rewrite left behind counts only where its files
// are a commit's of r.
func selectRecords(r *span, records []evidence.Record) []shown {
	trees, heads := map[string]string{}, map[string]string{}
	label := func(commit string) string {
		switch commit {
		case r.tip:
			return commit[:12] + " (TIP)"
		case r.base:
			return commit[:12] + " (BASE)"
		}
		return commit[:12]
	}
	out, _ := git.Run("-C", r.top, "log", "--format=%H %T", r.base+".."+r.tip)
	lines := strings.Split(out, "\n")
	if base, err := git.Run("-C", r.top, "log", "-1", "--format=%H %T", r.base); err == nil {
		lines = append(lines, base)
	}
	for _, line := range lines {
		if commit, tree, ok := strings.Cut(line, " "); ok {
			heads[commit] = label(commit)
			if trees[tree] == "" {
				trees[tree] = label(commit)
			}
		}
	}
	built := map[string]bool{}
	var picked []shown
	for _, rec := range records {
		at := ""
		switch {
		case rec.Tree != "" && trees[rec.Tree] != "":
			at = "the files of commit " + trees[rec.Tree]
		case heads[rec.Head] != "" && rec.Tree == "":
			at = "HEAD at commit " + heads[rec.Head] + "; its files were not recorded"
		case heads[rec.Head] != "":
			at = "HEAD at commit " + heads[rec.Head] + ", with files changed from it"
		case rec.Head != "":
			ok, seen := built[rec.Head]
			if !seen {
				ok = git.OK("-C", r.top, "merge-base", "--is-ancestor", r.tip, rec.Head)
				built[rec.Head] = ok
			}
			if ok {
				at = "commit " + rec.Head[:min(12, len(rec.Head))] + ", built on TIP"
			}
		}
		if at != "" {
			picked = append(picked, shown{Record: rec, id: recordID(rec), at: at})
		}
	}
	slices.SortStableFunc(picked, func(a, b shown) int { return a.Time.Compare(b.Time) })
	if len(picked) > maxRecords {
		picked = picked[len(picked)-maxRecords:]
	}
	return picked
}

// auditPrompt is the auditor's contract, the range, the claims and the
// evidence, with where saying where to read the change: nothing else of
// the author's.
func auditPrompt(r *span, list []string, records []shown, read int, sessions []string, where string) string {
	var b strings.Builder
	b.WriteString(ClaimsContract)
	fmt.Fprintf(&b, "\n# This audit\n\n## Range\n\nBASE..TIP is %s..%s (given as `%s`). Its commits, oldest first:\n\n%s\n\n%s\n",
		r.base, r.tip, r.given, r.commits, where)
	b.WriteString("\n## Claims\n")
	for i, c := range list {
		fmt.Fprintf(&b, "\n### C%d\n\n%s", i+1, fenced(redact.Secrets(c)))
	}
	fmt.Fprintf(&b, "\n## Evidence\n\nThe shell calls of session %s on this host, %d in all, of which these %d ran at a "+
		"commit of the range, at BASE, or on a commit built on TIP", strings.Join(sessions, ", "), read, len(records))
	if len(records) == maxRecords {
		fmt.Fprintf(&b, " (the newest %d)", maxRecords)
	}
	b.WriteString(":\n")
	if len(records) == 0 {
		b.WriteString("\nNone: every claim is unsupported.\n")
	}
	for _, s := range records {
		exit := "unknown: the harness reported none"
		switch {
		case s.Background:
			exit = "unknown: left running in the background, and no hook sees its end"
		case s.Exit != nil:
			exit = fmt.Sprint(*s.Exit)
		}
		fmt.Fprintf(&b, "\n### %s\n\n- time: %s\n- session: %s", s.id, s.Time.Format(time.RFC3339), s.Session)
		if s.AgentID != "" {
			fmt.Fprintf(&b, ", sub-agent %s", s.AgentID)
		}
		fmt.Fprintf(&b, "\n- ran at: %s, in %s\n- exit status: %s\n\nCommand:\n\n%s", s.at, s.Cwd, exit,
			fenced(redact.Secrets(s.Command)))
		if strings.TrimSpace(s.OutputTail) != "" {
			fmt.Fprintf(&b, "\nThe last lines of its output:\n\n%s", fenced(redact.Secrets(s.OutputTail)))
		}
	}
	b.WriteString("\nReply with the JSON object the Output section describes, and nothing else.\n")
	return b.String()
}

// judge reads an auditor's reply to the claims in list, given records. A
// claim the reply leaves out, or calls supported on no record shown, or
// only on records whose exit status is unknown, is unsupported.
func judge(reply string, list []string, records []shown) (*AuditOutput, error) {
	text := strings.TrimSpace(reply)
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return nil, errors.New("the reply holds no JSON object; ask the auditor again for the JSON the contract describes")
	}
	type verdict struct {
		ID      string   `json:"id"`
		Verdict string   `json:"verdict"`
		Records []string `json:"records"`
		Why     string   `json:"why"`
	}
	var parsed struct {
		Claims *[]verdict `json:"claims"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &parsed); err != nil || parsed.Claims == nil {
		return nil, fmt.Errorf("the reply is not the JSON the contract describes (%v); ask the auditor again", err)
	}
	known := map[string]shown{}
	for _, s := range records {
		known[s.id] = s
	}
	out := &AuditOutput{Claims: []AuditedClaim{}}
	for i, c := range list {
		a := AuditedClaim{ID: fmt.Sprintf("C%d", i+1), Claim: c, Verdict: "unsupported", Records: []string{}}
		at := slices.IndexFunc(*parsed.Claims, func(v verdict) bool { return v.ID == a.ID })
		if at < 0 {
			a.Why = "unsupported (no evidence): the auditor gave it no verdict"
			out.Claims = append(out.Claims, a)
			continue
		}
		got := (*parsed.Claims)[at]
		withStatus := false
		for _, id := range got.Records {
			if s, ok := known[id]; ok && !slices.Contains(a.Records, id) {
				a.Records = append(a.Records, id)
				withStatus = withStatus || s.Exit != nil
			}
		}
		switch {
		case got.Verdict != "supported":
			a.Why = got.Why
		case len(a.Records) == 0:
			a.Why = "unsupported (no evidence): it cites no record of the evidence log shown; the auditor said: " + got.Why
		case !withStatus:
			a.Why = "unsupported (no evidence): the records it rests on have no exit status (Codex's calls, or commands " +
				"left running in the background); the auditor said: " + got.Why
		default:
			a.Verdict, a.Why = "supported", got.Why
		}
		out.Claims = append(out.Claims, a)
	}
	return out, nil
}

// finishAudit logs the audit's receipt in the plan a review's would go to,
// and reports out.
func finishAudit(r *span, out *AuditOutput, hash string, sessions []string, nonce string,
	report func(any, error, int) int, log io.Writer) int {
	out.Range, out.Tip, out.ClaimsHash, out.Sessions = r.given, r.tip, hash, sessions
	unsupported := out.unsupported()
	id, err := git.PatchID([]string{"-C", r.top}, r.base, r.tip)
	file := ""
	if err == nil {
		model := ""
		if out.Model != nil {
			model = *out.Model
		}
		branch, _ := git.Run("-C", r.top, "branch", "--show-current")
		file, err = plans.WriteReceipt(os.Getenv("CLAUDE_CODE_SESSION_ID"), r.top, plans.Receipt{Range: r.given, Tip: r.tip,
			PatchID: id, Reviewer: out.Reviewer, Model: model, Findings: len(unsupported), Focus: claimsFocus,
			FindingIDs: unsupported, Branch: branch, Nonce: nonce, Claims: hash}, time.Now())
	}
	switch {
	case err != nil:
		fmt.Fprintf(log, "%s: no receipt of this audit was logged: %v\n", name, err)
	case file == "":
		fmt.Fprintf(log, "%s: no plan names this session, worktree or branch, so no receipt of this audit was logged\n", name)
	default:
		fmt.Fprintf(log, "%s: logged a receipt of this audit of %s in %s\n", name, r.given, file)
	}
	code := 0
	if len(unsupported) > 0 {
		code = 3
		fmt.Fprintf(log, "%s: %s unsupported: take each out, or run what shows it, and audit the claims again\n",
			name, strings.Join(unsupported, ", "))
		fmt.Fprintf(log, "%s: only the calls of session %s were read. Where a claim rests on another session's, such as "+
			"one before /clear, pass `--session ID` for each session, this one too: a log in %s is named ID.DAY.jsonl\n",
			name, strings.Join(sessions, ", "), evidence.Dir())
	}
	return report(out, nil, code)
}
