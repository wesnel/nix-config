package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/record"
	"github.com/wesnel/agent-tools/internal/redact"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/repos"
	"github.com/wesnel/agent-tools/internal/routes"
)

const name = "git-delegate"

// span is the range under review: as given, and resolved. For a pull
// request, slug and pr name it, and top is where it was fetched: a
// worktree, with nothing checked out, of the operator's clone (home), or
// its bare clone under Root.
type span struct {
	given, base, tip, top, common string
	commits                       string
	slug                          string
	pr                            int
	home                          bool
	// own is whether the change is the operator's: a range of this
	// repository, or a pull request whose author is the gh login's user.
	own bool
	// cleanup, where set, removes what resolving r made.
	cleanup func()
}

// release removes what resolving r made, once the review is done with it.
func (r *span) release() {
	if r.cleanup != nil {
		r.cleanup()
	}
}

// settings are the agent settings of the clone r is in: for a pull
// request, the operator's clone's, or none in its own bare clone.
func (r *span) settings() (*repos.Settings, error) {
	git.Dir = r.top
	defer func() { git.Dir = "" }()
	return repos.Lookup()
}

// resolve resolves the range given in the repository at dir, or, for "", the
// one containing the process's directory.
func resolve(dir, given string) (*span, error) {
	base, tip, ok := strings.Cut(given, "..")
	if !ok || strings.HasPrefix(tip, ".") || base == "" || tip == "" {
		return nil, refusal.New("%q is not a range BASE..TIP; pass the range whose commits make up the change, "+
			"such as `origin/main..HEAD`", given)
	}
	r := &span{given: given, top: dir}
	var err error
	if r.top == "" {
		if r.top, err = git.Toplevel(); err != nil {
			return nil, err
		}
	}
	if r.common, err = git.Run("-C", r.top, "rev-parse", "--path-format=absolute", "--git-common-dir"); err != nil {
		return nil, err
	}
	for _, p := range []struct {
		rev  string
		into *string
	}{{base, &r.base}, {tip, &r.tip}} {
		if *p.into, err = git.Run("-C", r.top, "rev-parse", "--verify", "--end-of-options", p.rev+"^{commit}"); err != nil {
			return nil, err
		}
	}
	if r.commits, err = git.Run("-C", r.top, "log", "--reverse", "--format=%H %s", r.base+".."+r.tip); err != nil {
		return nil, err
	}
	if r.commits == "" {
		return nil, refusal.New("%s has no commits to review; pass the range whose commits make up the change, "+
			"such as `origin/main..HEAD`", given)
	}
	return r, nil
}

// readFile reads path, or stdin for "-".
func readFile(path string, stdin io.Reader) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	return string(data), err
}

func generalReviewer() string {
	if os.Getenv("CODEX_SANDBOX") != "" || os.Getenv("CODEX_THREAD_ID") != "" {
		return "claude"
	}
	return "codex"
}

// unavailable says why Codex may not or cannot review from here, or "".
func unavailable(ctx context.Context, a *availability, auto bool) string {
	if err := a.check(codexProvider); err != nil {
		return err.Error()
	}
	switch {
	case os.Getenv("PR_LOOP_SESSION") != "":
		return "this is a review-loop session (PR_LOOP_SESSION is set), which reads other people's review text and sends none of it to OpenAI"
	case auto && generalReviewer() == "claude":
		return "the caller is Codex, so its second opinion needs another family or a fresh primary-harness sub-agent"
	}
	if err := a.admit(ctx, codexProvider); err != nil {
		return err.Error()
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return "codex is not on PATH"
	}
	if err := exec.Command("codex", "login", "status").Run(); err != nil {
		return "codex is not signed in (`codex login status` fails)"
	}
	return ""
}

type inputs struct {
	availability                 *availability
	r                            *span
	goal, items, prior, redCheck string
	// adequacy is what the reviewer is told of test adequacy, beside
	// red-check's results.
	adequacy string
	where    string
	// contract is a focused review's instructions, in place of Contract,
	// and focused the sections it adds: threat notes, the Reverting
	// section and overlapping branches.
	contract, focused string
	// description is the pull request description, and descriptionFrom
	// what it is, for a refusal to name; "" where none was given.
	description, descriptionFrom string
	// upgrades are an upgrade review's pins and what was gathered for
	// them, which raise its unread-upgrade findings.
	upgrades *upgrades
	// printed is whether the prompt is printed for a sub-agent, whose
	// reply delegate never sees.
	printed bool
}

// notesSection is the security prompt's threat notes, the operator's,
// fenced and redacted, or that there are none.
func notesSection(notes string) string {
	if strings.TrimSpace(notes) == "" {
		return "\n## Threat notes\n\nThe operator keeps no threat notes for this repository: the Checklist alone applies.\n"
	}
	return "\n## Threat notes\n\nThe operator's threat notes for this repository:\n\n" + fenced("text", redact.Secrets(notes))
}

// prompt is the contract, then the range, the goal statement, the items, the
// earlier findings, red-check's JSON and the findings it raised: nothing of
// the author's beyond them.
func prompt(in inputs) string {
	var b strings.Builder
	if in.contract != "" {
		b.WriteString(in.contract)
	} else {
		b.WriteString(Contract)
	}
	fmt.Fprintf(&b, "\n# This review\n\n## Range\n\nBASE..TIP is %s..%s (given as `%s`). Its commits, oldest first:\n\n%s\n\n%s\n",
		in.r.base, in.r.tip, in.r.given, in.r.commits, in.where)
	fmt.Fprintf(&b, "\n## Goal statement\n\n%s\n", redact.Secrets(strings.TrimSpace(in.goal)))
	// Items and earlier findings carry threads and CI logs.
	if s := strings.TrimSpace(in.items); s != "" {
		fmt.Fprintf(&b, "\n## Items\n\n%s\n", redact.Secrets(s))
	}
	if s := strings.TrimSpace(in.prior); s != "" {
		fmt.Fprintf(&b, "\n## Earlier findings\n\nAn earlier finding still standing is reported again, with `reopens` naming it.\n\n%s\n",
			redact.Secrets(s))
	}
	b.WriteString(in.focused)
	if in.redCheck != "" {
		fmt.Fprintf(&b, "\n## git red-check %s..%s\n\n%s", in.r.base, in.r.tip, redact.Secrets(in.redCheck))
	}
	if in.adequacy != "" {
		fmt.Fprintf(&b, "\n## Test adequacy\n\n%s", in.adequacy)
	}
	b.WriteString("\nReply with the JSON object the Output section describes, and nothing else.\n")
	return b.String()
}

// Codex is one confined, read-only Codex run.
type Codex struct {
	availability *availability
	// Checkout is a worktree the caller made and removes, whose parent
	// directory is the caller's own and takes the schema and the reply.
	Checkout string
	Prompt   string
	// Schema is the JSON schema the reply must fit.
	Schema  []byte
	Model   string
	Timeout time.Duration
	// Log receives Codex's output.
	Log io.Writer
	// Git runs git in the checkout; nil for the git package's Run, with
	// the user's config.
	Git func(args ...string) (string, error)
}

// CodexReply holds the reply and the explicitly requested model, if any.
// Codex's JSON events do not report its automatically selected model.
type CodexReply struct {
	Text, Model string
}

// CodexRun runs Codex with c's prompt in c's checkout, confined as
// confinement says. It returns an Unconfined where Codex's sandbox cannot
// start confined to the checkout.
func CodexRun(ctx context.Context, c Codex) (*CodexReply, error) {
	a := c.availability
	if a == nil {
		a = newAvailability()
	}
	if err := a.admit(ctx, codexProvider); err != nil {
		return nil, err
	}
	dir := filepath.Dir(c.Checkout)
	schema, reply := filepath.Join(dir, "reply.schema.json"), filepath.Join(dir, "reply.json")
	if err := os.WriteFile(schema, c.Schema, 0o644); err != nil { //nolint:gosec // a JSON schema compiled into this program
		return nil, err
	}
	run := c.Git
	if run == nil {
		run = func(args ...string) (string, error) { return git.Run(append([]string{"-C", c.Checkout}, args...)...) }
	}
	head, err := run("rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, err
	}
	admin, err := run("rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return nil, err
	}
	common, err := run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	// Codex runs helpers in its sandbox by executing itself, from the path
	// it was started by, so start it by a path the sandbox lets it read.
	codex, err := codexExecutable()
	if err != nil {
		return nil, err
	}
	confined, err := confinement(common, admin, codex)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	// Codex's sandbox does not start inside another macOS sandbox; Codex
	// must then not run at all, rather than run its commands unconfined.
	env := append(gitenv.Without(os.Environ()), "PATH="+realDirs(os.Getenv("PATH")))
	probe := exec.CommandContext(ctx, codex, append(append([]string{"sandbox"}, confined...),
		"-P", profile, "-C", c.Checkout, "--", "git", "rev-parse", "--verify", "HEAD")...)
	probe.Env = env
	var probeErr bytes.Buffer
	probe.Stderr = &probeErr
	if out, err := probe.Output(); err != nil || strings.TrimSpace(string(out)) != head {
		return nil, Unconfined(fmt.Sprintf("Codex's sandbox, confined to the worktree, could not run git there (%v: %s)",
			err, strings.TrimSpace(probeErr.String())))
	}
	args := append([]string{"exec", "--json", "--ignore-user-config", "--ignore-rules", "--ephemeral", "-C", c.Checkout,
		"--output-schema", schema, "-o", reply}, confined...)
	if c.Model != "" {
		args = append(args, "-m", c.Model)
	}
	cmd := exec.CommandContext(ctx, codex, append(args, "-")...)
	cmd.Dir = c.Checkout
	cmd.Env = env
	cmd.Stdin = strings.NewReader(c.Prompt)
	events := &quotaEvents{w: c.Log}
	cmd.Stdout, cmd.Stderr = events, c.Log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		stopGroup(cmd.Process.Pid, 5*time.Second)
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		if !events.discard {
			events.event(events.line)
		}
		if events.quota {
			return nil, a.exhausted(codexProvider)
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("codex stopped: %w", ctx.Err())
		}
		return nil, fmt.Errorf("codex: %w", err)
	}
	data, err := os.ReadFile(reply)
	if err != nil {
		return nil, fmt.Errorf("codex left no reply: %w", err)
	}
	return &CodexReply{Text: string(data), Model: c.Model}, nil
}

// stopGroup kills the process group pgid until it is empty, or for at most
// bound. One kill is not enough: a process that forks as the kill lands can
// leave a child in the group that the kill missed.
func stopGroup(pgid int, bound time.Duration) {
	deadline := time.Now().Add(bound)
	for syscall.Kill(-pgid, syscall.SIGKILL) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// Unconfined says why Codex cannot run confined.
type Unconfined string

func (e Unconfined) Error() string { return string(e) }

// profile names the Codex permissions profile confinement defines.
const profile = "delegate"

// confinement is the Codex configuration a review runs with: commands may
// read the worktree (Codex's workspace root), what macOS or Linux needs to
// run them (":minimal"), the Nix store, the codex executable and the parts
// of the git directory git needs to show the change, and nothing else; they
// may not write or reach the network. The user's configuration stays out
// with --ignore-user-config, so no MCP server or plugin of theirs loads, and
// the tools that read or reach out outside the sandbox are disabled.
func confinement(common, admin, codex string) ([]string, error) {
	var fs strings.Builder
	fs.WriteString(`{":minimal"="read", ":workspace_roots"={"."="read"}`)
	add := func(path, access string) error {
		if strings.ContainsFunc(path, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
			return fmt.Errorf("%q has a control character, which Codex's configuration cannot hold", path)
		}
		fmt.Fprintf(&fs, `, "%s"="%s"`, tomlEscape.Replace(path), access)
		return nil
	}
	reads := []string{"/nix/store", codex, admin}
	for _, p := range []string{"HEAD", "config", "objects", "refs", "packed-refs", "info", "shallow"} {
		reads = append(reads, filepath.Join(common, p))
	}
	// Temporary directories hold other programs' files, which :minimal
	// would let Codex read and write; a read granted above, inside one,
	// still holds, as the more specific rule.
	denies := []string{"/tmp", "/private/tmp", "/private/var/folders", filepath.Clean(os.TempDir())}
	for _, p := range reads {
		if err := add(p, "read"); err != nil {
			return nil, err
		}
	}
	for _, p := range denies {
		if err := add(p, "deny"); err != nil {
			return nil, err
		}
	}
	fs.WriteString("}")
	var gitEnv []string
	for _, kv := range gitenv.NoConfig {
		name, value, _ := strings.Cut(kv, "=")
		gitEnv = append(gitEnv, name+`="`+tomlEscape.Replace(value)+`"`)
	}
	args := []string{
		"-c", `default_permissions="` + profile + `"`,
		"-c", "permissions." + profile + ".filesystem=" + fs.String(),
		"-c", `shell_environment_policy.inherit="core"`,
		// Git cannot read the user's configuration in the sandbox, and
		// stops if told to.
		"-c", "shell_environment_policy.set={" + strings.Join(gitEnv, ", ") + "}",
		"-c", `web_search="disabled"`,
	}
	// Each must be a feature `codex features list` knows, or Codex refuses
	// to start.
	for _, feature := range []string{"apps", "browser_use", "browser_use_external", "computer_use", "hooks",
		"image_generation", "in_app_browser", "memories", "multi_agent", "plugins", "remote_plugin",
		"skill_mcp_dependency_install", "tool_suggest", "view_image"} {
		args = append(args, "--disable", feature)
	}
	return args, nil
}

// realDirs resolves the symbolic links in each directory of a PATH. Codex
// grants its sandbox the real paths of what it may read, so it cannot follow
// a link such as nix-darwin's /run, and Codex takes its shell from PATH.
func realDirs(path string) string {
	dirs := filepath.SplitList(path)
	for i, dir := range dirs {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dirs[i] = real
		}
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

// tomlEscape escapes text for the inside of a TOML basic string.
var tomlEscape = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// Finding is one problem a reviewer reports.
type Finding struct {
	// Finding is its ID in the review record, which settle takes.
	Finding  string  `json:"finding,omitempty"`
	ID       string  `json:"id"`
	Path     string  `json:"path"`
	Line     int     `json:"line"`
	EndLine  *int    `json:"endLine,omitempty"`
	Kind     string  `json:"kind"`
	Claim    string  `json:"claim"`
	Evidence string  `json:"evidence"`
	About    *string `json:"about,omitempty"`
	Reopens  *string `json:"reopens,omitempty"`
	// Reviewer is "red-check" for a finding the review raised itself from
	// red-check's verdicts, "upgrade" for one it raised for an unread pin,
	// and empty for the reviewer's.
	Reviewer string `json:"reviewer,omitempty"`
}

// Dropped is a finding left out of the result, and why.
type Dropped struct {
	Finding json.RawMessage `json:"finding"`
	Reason  string          `json:"reason"`
}

// Output is one reviewer type's part of a review.
type Output struct {
	Reviewer   string     `json:"reviewer"`
	Model      *string    `json:"model"`
	Range      string     `json:"range"`
	Tip        string     `json:"tip"`
	Findings   []*Finding `json:"findings"`
	Dropped    []Dropped  `json:"dropped"`
	Unverified []string   `json:"unverified"`
	// Routes are the focused reviews the change needs, where a general
	// review found any.
	Routes []routes.Route `json:"routes,omitempty"`
	// Focus is a focused review's route.
	Focus string `json:"focus,omitempty"`
	// Walkthrough is the guide's, in the order to read the change.
	Walkthrough []Chunk `json:"walkthrough,omitempty"`
	// Diagrams are the diagrams a diagram review kept, and diagrams those
	// the record keeps.
	Diagrams []DiagramResult `json:"diagrams,omitempty"`
	diagrams []record.Diagram
	// Upgrades are an upgrade review's account of each moved pin, from
	// its reply.
	Upgrades []UpgradeRead `json:"upgrades,omitempty"`
	// proposed are the routes the reviewer's reply named.
	proposed []proposedRoute
}

// proposedRoute is a route a reviewer's reply names.
type proposedRoute struct {
	Route string   `json:"route"`
	Why   string   `json:"why"`
	Paths []string `json:"paths"`
}

func (o *Output) exit() int {
	if len(o.Findings) > 0 {
		return 3
	}
	return 0
}

var kinds = map[string]bool{"goal-unmet": true, "bug": true, "regression": true, "unresolved": true,
	"out-of-scope": true, "untested": true, "untrue-doc": true, "convention": true}

// validate parses a reviewer's reply, which may be wrapped in prose or a
// code fence, and keeps the findings that fit the schema and point at a line
// that exists at TIP. A path inside wt, the checkout the reviewer read, is
// made relative to it. A focused review's reply, of focus fc named
// focusName, may hold its kinds too, must name every category of its
// checklist as checked, and has its findings' IDs prefixed with the
// focus, so that settling one never names a general review's.
func validate(reply string, r *span, wt, focusName string, fc *focus) (*Output, error) {
	blob := func(path string) ([]byte, error) {
		return git.Bytes(nil, "-C", r.top, "cat-file", "blob", r.tip+":"+path)
	}
	return validateWith(reply, r.given, r.tip, blob, wt, focusName, fc)
}

func validateWith(reply, given, tip string, blob func(path string) ([]byte, error), wt, focusName string,
	fc *focus) (*Output, error) {
	text := strings.TrimSpace(reply)
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return nil, errors.New("the reply holds no JSON object; ask the reviewer again for the JSON the contract describes")
	}
	var parsed struct {
		Findings   *[]json.RawMessage `json:"findings"`
		Unverified []string           `json:"unverified"`
		Routes     []proposedRoute    `json:"routes"`
		Checked    []string           `json:"checked"`
		Upgrades   []UpgradeRead      `json:"upgrades"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &parsed); err != nil || parsed.Findings == nil {
		return nil, fmt.Errorf("the reply is not the JSON the contract describes (%v); ask the reviewer again", err)
	}
	schema := Schema
	if fc != nil {
		schema = fc.schema()
	}
	item, err := checkReply(schema, []byte(text[start:end+1]))
	if err != nil {
		return nil, fmt.Errorf("the reply does not fit its schema: %v", err)
	}
	allowed := kinds
	if fc != nil {
		if left := fc.unchecked(parsed.Checked); len(left) > 0 {
			return nil, fmt.Errorf("the reply does not name as checked the categories %s of its checklist; "+
				"ask a new reviewer to go through every one", strings.Join(left, ", "))
		}
		allowed = maps.Clone(kinds)
		for _, k := range fc.kinds {
			allowed[k] = true
		}
		parsed.Routes = nil
	}
	out := &Output{Range: given, Tip: tip, Findings: []*Finding{}, Dropped: []Dropped{}, Unverified: []string{},
		proposed: parsed.Routes}
	if parsed.Unverified != nil {
		out.Unverified = parsed.Unverified
	}
	if fc != nil && fc.upgrades {
		out.Upgrades = parsed.Upgrades
	}
	seen := map[string]bool{}
	lines := map[string]int{}
	for _, raw := range *parsed.Findings {
		f, why := normalise(raw, blob, wt, lines, allowed)
		if why == "" {
			if err := item(raw); err != nil {
				why = "does not fit the schema: " + err.Error()
			}
		}
		if why == "" && focusName != "" {
			f.ID = focusName + "-" + f.ID
		}
		if why == "" && seen[f.ID] {
			why = "another finding has the id " + f.ID
		}
		if why != "" {
			out.Dropped = append(out.Dropped, Dropped{Finding: raw, Reason: why})
			continue
		}
		seen[f.ID] = true
		out.Findings = append(out.Findings, f)
	}
	return out, nil
}

// normalise checks one finding, returning it cleaned up or why it is
// dropped. lines caches each file's line count at TIP, -1 for no such file.
func normalise(raw json.RawMessage, blob func(path string) ([]byte, error), wt string, lines map[string]int,
	kinds map[string]bool) (*Finding, string) {
	var f Finding
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, "does not fit the schema: " + err.Error()
	}
	f.Reviewer, f.Finding = "", ""
	switch {
	case strings.TrimSpace(f.ID) == "":
		return nil, "has no id"
	case isOwn(f.ID):
		return nil, "ids starting " + mechanicalPrefix + " or " + unreadPrefix + " are the review's own"
	case !kinds[f.Kind]:
		return nil, fmt.Sprintf("kind %q is not one the contract defines", f.Kind)
	case strings.TrimSpace(f.Claim) == "" || strings.TrimSpace(f.Evidence) == "":
		return nil, "has no claim or no evidence"
	}
	path := f.Path
	if wt != "" && strings.HasPrefix(path, wt+string(filepath.Separator)) {
		path = strings.TrimPrefix(path, wt+string(filepath.Separator))
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if filepath.IsAbs(path) || path == "." || path == ".." || strings.HasPrefix(path, "../") {
		return nil, fmt.Sprintf("%s is not a path in the repository", f.Path)
	}
	f.Path = path
	n, ok := lines[path]
	if !ok {
		n = -1
		if data, err := blob(path); err == nil {
			n = bytes.Count(data, []byte("\n"))
			if len(data) > 0 && data[len(data)-1] != '\n' {
				n++
			}
		}
		lines[path] = n
	}
	switch {
	case n < 0:
		return nil, fmt.Sprintf("%s does not exist at TIP", path)
	case f.Line < 1 || f.Line > n:
		return nil, fmt.Sprintf("%s has %d lines at TIP, so line %d does not exist", path, n, f.Line)
	}
	if f.EndLine != nil && (*f.EndLine < f.Line || *f.EndLine > n) {
		f.EndLine = nil
	}
	for _, p := range []**string{&f.About, &f.Reopens} {
		if *p != nil && strings.TrimSpace(**p) == "" {
			*p = nil
		}
	}
	return &f, ""
}
