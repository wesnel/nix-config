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
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/guard"
	"github.com/wesnel/agent-tools/internal/redact"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// AskUsage is `git delegate ask -h`.
const AskUsage = `usage: git delegate ask [--reviewer TYPE] [--print-prompt] --question FILE [--model M] [--timeout S] PATH...

Asks the other model family (Claude for a Codex caller, Codex otherwise)
for a second opinion on a design
question: the question in FILE, judged from the files PATH names in this
repository and nothing else. FILE, where its symlinks lead, is in this
repository or under $TMPDIR or /tmp, and is no credential the guard
lists; the reviewer is sent it with only known secret shapes taken out,
as it is sent the files. It runs as a review of that TYPE does (git
delegate -h), in a throwaway repository under the git directory's
delegate/ holding copies of those files, removed on every exit. It
writes no record. --print-prompt exports the same contract, question and
redacted file snapshots for a fresh primary-harness sub-agent, without
starting a provider. Reviewer availability is shared with every review.

Writes JSON on stdout: reviewer, model, answer and unverified (what the
answer rests on that the files do not show). Exit status: 0 answered; 4
the reviewer cannot answer here, for the reasons a review exits 4; 1 the
question could not be asked; 2 a usage error or refusal.
`

// askContract is what the reviewer is told before the question.
const askContract = `You give an independent second opinion on one design question.
The working directory or supplied file snapshots hold copies of the files the
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
	reviewer := flags.String("reviewer", generalReviewer(), "")
	question := flags.String("question", "", "")
	model := flags.String("model", "", "")
	timeout := flags.Int("timeout", 900, "")
	printPrompt := flags.Bool("print-prompt", false, "")
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
	text, err := questionText(top, *question, stdin)
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
	if *printPrompt {
		fmt.Fprint(stdout, askPrompt(text, files))
		for _, f := range files {
			fmt.Fprintf(stdout, "\n## File: %s\n\n%s", f.rel, fenced("text", redact.Secrets(string(f.data))))
		}
		return 0
	}
	why := ""
	a := newAvailability()
	p := codexProvider
	if ty.claude {
		p = claudeProvider
	}
	if err := a.check(p); err != nil {
		why = err.Error()
	} else if ty.claude {
		why = claudeMissing()
	} else {
		why = unavailable(ctx, a, true)
	}
	if why != "" {
		fmt.Fprintf(stderr, "%s: the %s reviewer cannot answer here: %s. Ask a new sub-agent that only reads instead "+
			"in the primary harness with a fresh context. Add `--print-prompt` to this ask command to export its contract and file snapshots from %s and the same files, with none of your own reasoning. In review-loop sessions use the operator-selected harness.\n",
			name, *reviewer, why, *question)
		return 4
	}
	out, err := askIn(ctx, top, text, files, *reviewer, ty, *model, time.Duration(*timeout)*time.Second, stderr, a)
	var unavailable providerUnavailable
	if errors.As(err, &unavailable) {
		fmt.Fprintf(stderr, "%s: %s. Add `--print-prompt` to this ask command and give the result to a fresh read-only sub-agent in the primary harness instead, with none of your reasoning. In review-loop sessions use the operator-selected harness.\n", name, err)
		return 4
	}
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

// questionText is the question in the file path names, or on stdin for
// "-". Codex is sent it as it is, so the file, where its symlinks lead,
// must be in the repository at top or a scratch root, and not a
// credential.
func questionText(top, path string, stdin io.Reader) (string, error) {
	if path == "-" {
		data, err := io.ReadAll(stdin)
		return string(data), err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	f, real, err := openRegular(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
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
	data, err := io.ReadAll(f)
	return string(data), err
}

// askFile is a file a question names: its path relative to the repository,
// and what it held when named checked it.
type askFile struct {
	rel  string
	data []byte
}

// named reads each path, relative to the process's directory or absolute,
// refusing one that is not a regular file in the repository at top. top
// has its symlinks resolved, and the check is made on the path the kernel
// has for the file opened, so a link leading out of the repository is
// refused and what is read is the file checked.
func named(top string, paths []string) ([]askFile, error) {
	var out []askFile
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		f, real, err := openRegular(abs)
		var r refusal.Refusal
		if errors.As(err, &r) {
			return nil, err
		}
		if err != nil {
			return nil, refusal.New("%s is not a file; name each file the answer rests on, as `git delegate ask "+
				"--question FILE PATH...`", p)
		}
		rel, err := filepath.Rel(top, real)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			f.Close()
			return nil, refusal.New("%s is outside the repository at %s; put what the question needs from it into the "+
				"question file, and name only files in the repository, as `git delegate ask --question FILE PATH...`", p, top)
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, askFile{filepath.ToSlash(rel), data})
	}
	return out, nil
}

// openRegular opens path to read, if it is a regular file, and returns it
// with the path the kernel has for the file opened, symlinks resolved. A
// check made on that path holds for what is read from the file, which a
// link put in place after the open cannot change. A FIFO is refused
// without waiting for a writer. A file with more than one hard link is
// refused: that path is only one of its names, any of which may be
// anywhere on its file system.
func openRegular(path string) (*os.File, string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", err
	}
	real, err := openedPath(f)
	if err == nil {
		var info os.FileInfo
		if info, err = f.Stat(); err == nil {
			err = regularOnly(path, info)
		}
	}
	if err != nil {
		f.Close()
		return nil, "", err
	}
	return f, real, nil
}

// regularOnly is why openRegular refuses the file at path, or nil.
func regularOnly(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", path)
	case !ok || st.Nlink > 1:
		return refusal.New("%s has other hard links, which may be anywhere, so where it is cannot be told from its "+
			"name; put what the question needs from it into a file of its own, and pass that to `git delegate ask`", path)
	}
	return nil
}

// askIn writes files, redacted, into a throwaway repository under the git
// directory's delegate/, has the reviewer of type ty answer text there,
// redacted too, confined as a review of that type is, and removes the
// repository however it ends.
func askIn(ctx context.Context, top, text string, files []askFile, reviewer string, ty reviewerType, model string,
	timeout time.Duration, log io.Writer, a *availability) (*Answer, error) {
	common, err := git.Run("-C", top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	parent := filepath.Join(common, "delegate")
	if err := os.MkdirAll(parent, 0o755); err != nil { //nolint:gosec // holds copies of the repository's files, in its git directory
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
		to := filepath.Join(tree, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil { //nolint:gosec // holds copies of the repository's files, in its git directory
			return nil, err
		}
		if err := os.WriteFile(to, []byte(redact.Secrets(string(f.data))), 0o644); err != nil { //nolint:gosec // named refuses an f that leaves the repository, so to stays in tree
			return nil, err
		}
	}
	// A repository of its own, which none of the user's configuration
	// reaches, so that committing signs nothing and runs no hook.
	run := func(args ...string) (string, error) {
		return git.RunEnv(gitenv.NoConfig,
			append([]string{"-C", tree, "-c", "user.name=delegate", "-c", "user.email=delegate@localhost",
				"-c", "commit.gpgsign=false"}, args...)...)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "The files the question names"}} {
		if _, err := run(args...); err != nil {
			return nil, err
		}
	}

	var reply *CodexReply
	if ty.claude {
		reply, err = ClaudeRun(ctx, Claude{Checkout: tree, Agent: ty.agent, Prompt: askPrompt(text, files), Schema: []byte(askSchema),
			Timeout: timeout, Log: log, availability: a})
	} else {
		reply, err = CodexRun(ctx, Codex{Checkout: tree, Prompt: askPrompt(text, files), Schema: []byte(askSchema), Model: model,
			Timeout: timeout, Log: log, Git: run, availability: a})
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

func askPrompt(text string, files []askFile) string {
	var prompt strings.Builder
	prompt.WriteString(askContract)
	fmt.Fprintf(&prompt, "\n# The question\n\n%s\n\n# The files\n\n", redact.Secrets(strings.TrimSpace(text)))
	for _, f := range files {
		fmt.Fprintf(&prompt, "- %s\n", f.rel)
	}
	return prompt.String()
}
