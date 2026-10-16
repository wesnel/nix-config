package review

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/guard"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// AskUsage is `git fresh-review ask -h`.
const AskUsage = `usage: git fresh-review ask --question FILE [--model M] [--timeout S] PATH...

Asks Codex, as a model family other than the author's, for a second opinion
on a design question: the question in FILE, judged from the files PATH
names in this repository and nothing else. FILE, where its symlinks lead,
is in this repository or under $TMPDIR or /tmp, and is no credential the
guard lists, since Codex is sent it as it is. Codex runs as a review does
(git fresh-review -h): no configuration of the user's, no network, no
writes, and its commands may read only a throwaway checkout under the git
directory's fresh-review/ holding copies of those files, removed on every
exit.

Writes JSON on stdout: reviewer, model, answer and unverified (what the
answer rests on that the files do not show). Exit status: 0 answered; 4
Codex cannot answer here, for the reasons a review exits 4; 1 the question
could not be asked; 2 a usage error or refusal.
`

// askContract is what Codex is told before the question.
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

// Answer is what `git fresh-review ask` prints.
type Answer struct {
	Reviewer   string   `json:"reviewer"`
	Model      *string  `json:"model"`
	Answer     string   `json:"answer"`
	Unverified []string `json:"unverified"`
}

// ask runs `git fresh-review ask` with args and returns its exit status.
func ask(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(name+" ask", flag.ContinueOnError)
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
	if *question == "" {
		return report(nil, refusal.New("a second opinion answers a question; write it to a file and pass `--question FILE`"), 0)
	}
	if len(paths) == 0 {
		return report(nil, refusal.New("Codex reads only the files a question names, and none is named; "+
			"pass each file the answer rests on, as `git fresh-review ask --question %s PATH...`", *question), 0)
	}
	if err := git.CheckConfig(""); err != nil {
		return report(nil, err, 0)
	}
	top, err := git.Toplevel()
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
	if why := unavailable(true); why != "" {
		fmt.Fprintf(stderr, "%s: Codex cannot answer here: %s. Ask a new sub-agent that only reads instead "+
			"(Claude Code's `Plan` agent, ECA's `explorer`), giving it %s and the same files, and nothing of your own reasoning.\n",
			name, why, *question)
		return 4
	}
	out, err := codexAsk(ctx, top, text, files, *model, time.Duration(*timeout)*time.Second, stderr)
	var unconfined Unconfined
	if errors.As(err, &unconfined) {
		fmt.Fprintf(stderr, "%s: Codex cannot answer here: %s. Ask a new sub-agent that only reads instead, "+
			"giving it %s and the same files; or report that `git fresh-review ask` needs running outside this sandbox.\n",
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
// refusing one that is not a regular file in the repository at top.
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
			return nil, refusal.New("%s is not a file; name each file the answer rests on, as `git fresh-review ask "+
				"--question FILE PATH...`", p)
		}
		rel, err := filepath.Rel(top, real)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			f.Close()
			return nil, refusal.New("%s is outside the repository at %s; put what the question needs from it into the "+
				"question file, and name only files in the repository, as `git fresh-review ask --question FILE PATH...`", p, top)
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
			"name; put what the question needs from it into a file of its own, and pass that to `git fresh-review ask`", path)
	}
	return nil
}

// codexAsk writes files into a throwaway repository under the git
// directory's fresh-review/, has Codex answer text there, confined as
// CodexRun confines a review, and removes the repository however it ends.
func codexAsk(ctx context.Context, top, text string, files []askFile, model string, timeout time.Duration,
	log io.Writer) (*Answer, error) {
	common, err := git.Run("-C", top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	parent := filepath.Join(common, "fresh-review")
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
		if err := os.WriteFile(to, f.data, 0o644); err != nil { //nolint:gosec // named refuses an f that leaves the repository, so to stays in tree
			return nil, err
		}
	}
	// A repository of its own, which none of the user's configuration
	// reaches, so that committing signs nothing and runs no hook.
	run := func(args ...string) (string, error) {
		return git.RunEnv([]string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"},
			append([]string{"-C", tree, "-c", "user.name=fresh-review", "-c", "user.email=fresh-review@localhost",
				"-c", "commit.gpgsign=false"}, args...)...)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "The files the question names"}} {
		if _, err := run(args...); err != nil {
			return nil, err
		}
	}

	var prompt strings.Builder
	prompt.WriteString(askContract)
	fmt.Fprintf(&prompt, "\n# The question\n\n%s\n\n# The files\n\n", strings.TrimSpace(text))
	for _, f := range files {
		fmt.Fprintf(&prompt, "- %s\n", f.rel)
	}
	reply, err := CodexRun(ctx, Codex{Checkout: tree, Prompt: prompt.String(), Schema: []byte(askSchema), Model: model,
		Timeout: timeout, Log: log, Git: run})
	if err != nil {
		return nil, err
	}
	out := &Answer{Reviewer: "codex", Unverified: []string{}}
	if err := json.Unmarshal([]byte(strings.TrimSpace(reply.Text)), out); err != nil || strings.TrimSpace(out.Answer) == "" {
		return nil, fmt.Errorf("Codex's reply is not the answer asked for (%v): %s", err, reply.Text)
	}
	out.Reviewer = "codex"
	if out.Unverified == nil {
		out.Unverified = []string{}
	}
	if reply.Model != "" {
		out.Model = &reply.Model
	}
	return out, nil
}
