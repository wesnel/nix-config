package review

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/guard"
	"github.com/wesnel/agent-tools/internal/redact"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// AskUsage is `git delegate ask -h`.
const AskUsage = `usage: git delegate ask [--reviewer TYPE] --question FILE [--model M] [--timeout S] PATH...

Asks a reviewer that did not write the change, Codex by default, as a
model family other than the author's, for a second opinion on a design
question: the question in FILE, judged from the files PATH names in this
repository and nothing else. FILE, where its symlinks lead, is in this
repository or under $TMPDIR or /tmp, and is no credential the guard
lists; the reviewer is sent it with only known secret shapes taken out,
as it is sent the files. It runs as a review of that TYPE does (git
delegate -h), in a throwaway repository under the git directory's
delegate/ holding copies of those files, removed on every exit. It
writes no record.

Writes JSON on stdout: reviewer, model, answer and unverified (what the
answer rests on that the files do not show). Exit status: 0 answered; 4
the reviewer cannot answer here, for the reasons a review exits 4; 1 the
question could not be asked; 2 a usage error or refusal.
`

// askContract is what the reviewer is told before the question.
const askContract = `You give a second opinion on one design question, from another model family
than the author's. The working directory holds copies of the files the
question names, and nothing else. Only read: never edit a file, run the
code, or reach anything outside the working directory.

Judge from what the files say, not from what the question hopes. Answer
with your choice, the reasons for it, and what it risks. Where the answer
rests on something the files do not show, name it in unverified.

Reply with one JSON object and nothing else:
{"answer": "...", "unverified": ["..."]}
`

// askSchema is the JSON Schema of Codex's answer, in the strict subset
// Codex's --output-schema takes.
const askSchema = `{"type": "object", "additionalProperties": false, "required": ["answer", "unverified"],
 "properties": {"answer": {"type": "string"}, "unverified": {"type": "array", "items": {"type": "string"}}}}`

// Answer is what `git delegate ask` prints.
type Answer struct {
	Reviewer   string   `json:"reviewer"`
	Model      *string  `json:"model"`
	Answer     string   `json:"answer"`
	Unverified []string `json:"unverified"`
}

// ask runs `git delegate ask` with args and returns its exit status.
func ask(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(name+" ask", flag.ContinueOnError)
	reviewer := flags.String("reviewer", "codex", "")
	question := flags.String("question", "", "")
	model := flags.String("model", "", "")
	timeout := flags.Int("timeout", 900, "")
	var paths []string
	for {
		if code, ok := cli.Parse(flags, args, AskUsage, stdout, stderr); !ok {
			return code
		}
		if flags.NArg() == 0 {
			break
		}
		paths = append(paths, flags.Arg(0))
		args = flags.Args()[1:]
	}
	report := func(result any, err error, code int) int {
		return cli.Report(name, result, err, code, stdout, stderr)
	}
	ty, ok := reviewerTypes[*reviewer]
	if !ok {
		return report(nil, refusal.New("%q is not a reviewer type; pass `--reviewer` with one of %s", *reviewer,
			strings.Join(slices.Sorted(maps.Keys(reviewerTypes)), ", ")), 0)
	}
	if *question == "" {
		return report(nil, refusal.New("a second opinion answers a question; write it to a file under the working "+
			"directory and pass `--question FILE`"), 0)
	}
	if len(paths) == 0 {
		return report(nil, refusal.New("the reviewer reads only the files a question names, and none is named; "+
			"pass each file the answer rests on, as `git delegate ask --question %s PATH...`", *question), 0)
	}
	if err := git.CheckConfig(""); err != nil {
		return report(nil, err, 0)
	}
	top, err := git.Toplevel()
	if err == nil {
		top, err = filepath.EvalSymlinks(top)
	}
	if err != nil {
		return report(nil, err, 0)
	}
	file, err := questionFile(top, *question)
	if err != nil {
		return report(nil, err, 0)
	}
	text, err := readFile(file, stdin)
	if err != nil {
		return report(nil, err, 0)
	}
	if strings.TrimSpace(text) == "" {
		return report(nil, refusal.New("the question %s is empty; write the question into it and pass `--question %s` again",
			*question, *question), 0)
	}
	files, err := named(top, paths)
	if err != nil {
		return report(nil, err, 0)
	}
	why := ""
	if ty.claude {
		why = claudeMissing()
	} else {
		why = unavailable(true)
	}
	if why != "" {
		fmt.Fprintf(stderr, "%s: the %s reviewer cannot answer here: %s. Ask a new sub-agent that only reads instead "+
			"(Claude Code's `design-reviewer` agent), giving it %s and the same files, and nothing of your own reasoning.\n",
			name, *reviewer, why, *question)
		return 4
	}
	out, err := askIn(ctx, top, text, files, *reviewer, ty, *model, time.Duration(*timeout)*time.Second, stderr)
	var unconfined Unconfined
	if errors.As(err, &unconfined) {
		fmt.Fprintf(stderr, "%s: Codex cannot answer here: %s. Ask a new sub-agent that only reads instead, "+
			"giving it %s and the same files; or report that `git delegate ask` needs running outside this sandbox.\n",
			name, unconfined, *question)
		return 4
	}
	if err != nil {
		return report(nil, err, 0)
	}
	return report(out, nil, 0)
}

// scratchRoots are the directories outside the repository a question may
// be written in.
var scratchRoots = guard.ScratchRoots

// questionFile is the question file path names, "-" for stdin, with its
// symlinks resolved. Codex is sent it as it is, so it must be in the
// repository at top or a scratch root, and not a credential.
func questionFile(top, path string) (string, error) {
	if path == "-" {
		return path, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	write := "write the question to a file of its own under $TMPDIR or in the repository, and pass that as `--question FILE`"
	for _, p := range guard.CredentialPaths() {
		if strings.HasPrefix(abs, p) || strings.HasPrefix(real, p) {
			return "", refusal.New("%s is a credential, which stays out of what Codex is sent; %s", path, write)
		}
	}
	within := func(root string) bool {
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
		return real == root || strings.HasPrefix(real, strings.TrimSuffix(root, "/")+"/")
	}
	if !slices.ContainsFunc(append([]string{top}, scratchRoots()...), within) {
		return "", refusal.New("%s is outside the repository and the scratch area ($TMPDIR, /tmp), and Codex is sent the question as it is; %s",
			path, write)
	}
	return real, nil
}

// named is each path, relative to the process's directory or absolute,
// as a path relative to top, refusing one that is not a regular file in
// the repository at top. top has its symlinks resolved, and so has each
// path before it is checked, so a link leading out of the repository is
// refused and the path returned, joined to top, is the file checked.
func named(top string, paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		abs, err = filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, refusal.New("%s is not a file; name each file the answer rests on, as `git delegate ask "+
				"--question FILE PATH...`", p)
		}
		rel, err := filepath.Rel(top, abs)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, refusal.New("%s is outside the repository at %s; put what the question needs from it into the "+
				"question file, and name only files in the repository, as `git delegate ask --question FILE PATH...`", p, top)
		}
		if info, err := os.Stat(abs); err != nil || !info.Mode().IsRegular() {
			return nil, refusal.New("%s is not a file; name each file the answer rests on, as `git delegate ask "+
				"--question FILE PATH...`", p)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, nil
}

// askIn copies files from top, redacted, into a throwaway repository under
// the git directory's delegate/, has the reviewer of type ty answer text
// there, redacted too, confined as a review of that type is, and removes
// the repository however it ends.
func askIn(ctx context.Context, top, text string, files []string, reviewer string, ty reviewerType, model string,
	timeout time.Duration, log io.Writer) (*Answer, error) {
	common, err := git.Run("-C", top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	parent := filepath.Join(common, "delegate")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(parent, "ask-")
	if err != nil {
		return nil, err
	}
	defer func() {
		os.RemoveAll(dir)
		// Left in place while a review is using it.
		os.Remove(parent)
	}()
	tree := filepath.Join(dir, "tree")
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(top, filepath.FromSlash(f)))
		if err != nil {
			return nil, err
		}
		to := filepath.Join(tree, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(to, []byte(redact.Secrets(string(data))), 0o644); err != nil { //nolint:gosec // named refuses an f that leaves the repository, so to stays in tree
			return nil, err
		}
	}
	// A repository of its own, which none of the user's configuration
	// reaches, so that committing signs nothing and runs no hook.
	run := func(args ...string) (string, error) {
		return git.RunEnv([]string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"},
			append([]string{"-C", tree, "-c", "user.name=delegate", "-c", "user.email=delegate@localhost",
				"-c", "commit.gpgsign=false"}, args...)...)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "The files the question names"}} {
		if _, err := run(args...); err != nil {
			return nil, err
		}
	}

	var prompt strings.Builder
	prompt.WriteString(askContract)
	fmt.Fprintf(&prompt, "\n# The question\n\n%s\n\n# The files\n\n", redact.Secrets(strings.TrimSpace(text)))
	for _, f := range files {
		fmt.Fprintf(&prompt, "- %s\n", f)
	}
	var reply *CodexReply
	if ty.claude {
		reply, err = ClaudeRun(ctx, Claude{Checkout: tree, Agent: ty.agent, Prompt: prompt.String(), Schema: []byte(askSchema),
			Timeout: timeout, Log: log})
	} else {
		reply, err = CodexRun(ctx, Codex{Checkout: tree, Prompt: prompt.String(), Schema: []byte(askSchema), Model: model,
			Timeout: timeout, Log: log, Git: run})
	}
	if err != nil {
		return nil, err
	}
	out := &Answer{Unverified: []string{}}
	if err := json.Unmarshal([]byte(strings.TrimSpace(reply.Text)), out); err != nil || strings.TrimSpace(out.Answer) == "" {
		return nil, fmt.Errorf("the %s reply is not the answer asked for (%v): %s", reviewer, err, reply.Text)
	}
	out.Reviewer = reviewer
	if out.Unverified == nil {
		out.Unverified = []string{}
	}
	if reply.Model != "" {
		out.Model = &reply.Model
	}
	return out, nil
}
