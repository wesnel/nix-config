// Package redcheck checks that the tests a commit adds or changes fail
// without the commit's other changes and pass with them.
package redcheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// Verdicts, one per checked commit.
const (
	// The tests fail without the change, the build command succeeding there,
	// and pass with it.
	FailsAsAsserted = "fails-as-asserted"
	// The tests fail without the change and pass with it, but with no build
	// command a build failure cannot be told from a failed assertion, so
	// this is no stronger than FailsToBuildWithoutChange.
	FailsWithoutChange = "fails-without-change"
	// The build command fails without the change: the tests need the new
	// code to build, which shows they use it, not that they check it.
	FailsToBuildWithoutChange = "fails-to-build-without-change"
	// The tests pass without the change, so they do not test it.
	PassesWithoutChange = "passes-without-change"
	// The tests fail at the commit itself.
	FailsWithChange = "fails-with-change"
	// The commit changes no test files.
	NoTestFiles = "no-test-files"
	// The commit changes only test files, so there is no change to test.
	NoCodeChange = "no-code-change"
)

// DefaultLines is how many lines of each run's output are kept.
const DefaultLines = 40

// Exit statuses for a completed check, worst verdict first.
const (
	ExitProven    = 0
	ExitDisproven = 3
	ExitUnproven  = 4
)

// Messages explains each verdict that is not a pass.
var Messages = map[string]string{
	PassesWithoutChange:       "the new tests do not test this change: they pass without it",
	FailsWithChange:           "the tests fail with the change",
	FailsToBuildWithoutChange: "the tests fail to build without the change, which is weaker evidence than a failed assertion",
	FailsWithoutChange:        "the tests fail without the change, but with no build command given that may be only a failure to build; pass --build or configure one",
	NoTestFiles:               "no test files changed",
	NoCodeChange:              "only test files changed, so there is no change for them to test",
}

// Exit is the exit status a verdict calls for.
func Exit(verdict string) int {
	switch verdict {
	case FailsAsAsserted:
		return ExitProven
	case PassesWithoutChange, FailsWithChange:
		return ExitDisproven
	}
	return ExitUnproven
}

// Options says what to check and how.
type Options struct {
	// Dir is any directory in the repository; empty means the process's.
	Dir string
	// Test is the test command, run with sh at the top of the checkout.
	Test string
	// Build, if set, is run where Test fails without the change, to tell a
	// build failure from a failed assertion.
	Build string
	// TestFiles are git pathspec globs (`:(glob)` magic) for test files.
	TestFiles []string
	// Fixups keeps only a range's fixup!, amend! and squash! commits.
	Fixups bool
	// Lines is how much of the end of each run's output to keep; zero
	// means DefaultLines.
	Lines int
	// Log receives progress lines.
	Log io.Writer
}

// Run is one run of a command in the throwaway checkout.
type Run struct {
	Exit   int    `json:"exit"`
	Output string `json:"output"`
}

// Check is the outcome for one commit.
type Check struct {
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
	Verdict string `json:"verdict"`
	// Why says what a verdict other than fails-as-asserted lacks.
	Why       string   `json:"why,omitempty"`
	TestFiles []string `json:"testFiles"`
	// Build is the build command without the change; Without and With are
	// the test command without and with it.
	Build   *Run `json:"buildWithoutChange,omitempty"`
	Without *Run `json:"withoutChange,omitempty"`
	With    *Run `json:"withChange,omitempty"`
}

// Result is the outcome for every checked commit, oldest first.
type Result struct {
	Test      string   `json:"test"`
	Build     string   `json:"build,omitempty"`
	TestFiles []string `json:"testFiles"`
	Commits   []*Check `json:"commits"`
}

// Exit is the exit status the worst of the result's verdicts calls for.
func (r *Result) Exit() int {
	code := ExitProven
	for _, c := range r.Commits {
		switch e := Exit(c.Verdict); {
		case e == ExitDisproven:
			return e
		case e == ExitUnproven:
			code = e
		}
	}
	return code
}

// Do checks rev: a commit, against its parent, or a range A..B, each
// non-merge commit in it against its own parent.
//
// Each commit is checked in its own detached worktree under the repository's
// git directory, which a sandbox that lets git create a worktree at all lets
// it write. The worktree is removed afterwards, also on error or when ctx is
// cancelled. No ref, index or file of the caller's worktrees is touched.
func Do(ctx context.Context, rev string, opts Options) (*Result, error) {
	if opts.Test == "" {
		return nil, refusal.New("no test command is configured for this clone, so no test ran; pass `--test CMD`, " +
			"or ask the operator to set \"test\" in the clone's agent settings, .git/info/agents.json")
	}
	if len(opts.TestFiles) == 0 {
		return nil, refusal.New("no test file globs are configured for this clone, so no test ran; pass `--test-files GLOB`, " +
			"or ask the operator to set \"testFiles\" in the clone's agent settings, .git/info/agents.json")
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if opts.Lines <= 0 {
		opts.Lines = DefaultLines
	}
	var top string
	var err error
	if opts.Dir != "" {
		top, err = git.Run("-C", opts.Dir, "rev-parse", "--show-toplevel")
	} else {
		top, err = git.Toplevel()
	}
	if err != nil {
		return nil, err
	}
	common, err := git.Run("-C", top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	// Once the repository is found, neither these tools' git nor the test
	// command may reach the caller's tree.
	gitenv.Clear()
	commits, err := commitsOf(top, rev, opts.Fixups)
	if err != nil {
		return nil, err
	}
	result := &Result{Test: opts.Test, Build: opts.Build, TestFiles: opts.TestFiles, Commits: []*Check{}}
	for _, c := range commits {
		check, err := checkCommit(ctx, top, common, c, opts)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", short(c), err)
		}
		check.Why = Messages[check.Verdict]
		result.Commits = append(result.Commits, check)
	}
	return result, nil
}

// commitsOf lists the commits rev names, oldest first.
func commitsOf(top, rev string, fixups bool) ([]string, error) {
	if !strings.Contains(rev, "..") {
		if fixups {
			return nil, refusal.New("--fixups picks the fixups out of a range, so it needs one; pass one such as `origin/main..HEAD`")
		}
		commit, err := git.Run("-C", top, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
		if err != nil {
			return nil, err
		}
		return []string{commit}, nil
	}
	out, err := git.Run("-C", top, "rev-list", "--reverse", "--no-merges", "--format=%H %s", "--end-of-options", rev)
	if err != nil {
		return nil, err
	}
	commits := []string{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, "commit ") {
			continue
		}
		hash, subject, _ := strings.Cut(line, " ")
		if fixups && !isFixup(subject) {
			continue
		}
		commits = append(commits, hash)
	}
	return commits, nil
}

func isFixup(subject string) bool {
	for _, prefix := range []string{"fixup! ", "amend! ", "squash! "} {
		if strings.HasPrefix(subject, prefix) {
			return true
		}
	}
	return false
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

func checkCommit(ctx context.Context, top, common, commit string, opts Options) (*Check, error) {
	info, err := git.Run("-C", top, "log", "-1", "--format=%P%n%s", commit)
	if err != nil {
		return nil, err
	}
	parents, subject, _ := strings.Cut(info, "\n")
	check := &Check{Commit: commit, Subject: subject, TestFiles: []string{}}
	switch n := len(strings.Fields(parents)); {
	case n == 0:
		return nil, refusal.New("%s is a root commit, with no parent to test without it; "+
			"say in the report that its tests cannot be red-checked", short(commit))
	case n > 1:
		return nil, refusal.New("%s is a merge, which has no single change to test; "+
			"run `git red-check` on the commits it brings in instead", short(commit))
	}
	parent := strings.Fields(parents)[0]

	changed, err := changedFiles(top, parent, commit, nil)
	if err != nil {
		return nil, err
	}
	globs := make([]string, len(opts.TestFiles))
	for i, g := range opts.TestFiles {
		globs[i] = ":(glob)" + g
	}
	tests, err := changedFiles(top, parent, commit, globs)
	if err != nil {
		return nil, err
	}
	check.TestFiles = tests
	switch {
	case len(tests) == 0:
		check.Verdict = NoTestFiles
		return check, nil
	case len(tests) == len(changed):
		check.Verdict = NoCodeChange
		return check, nil
	}

	wt, err := addWorktree(top, common, parent)
	if err != nil {
		return nil, err
	}
	defer wt.remove(top)

	// The parent with the commit's test files: what the tests see without
	// the change. Restoring a file the commit deletes deletes it.
	literal := make([]string, len(tests))
	for i, f := range tests {
		literal[i] = ":(literal)" + f
	}
	if _, err := git.Run(append([]string{"-C", wt.path, "restore", "--source", commit, "--staged", "--worktree", "--"}, literal...)...); err != nil {
		return nil, err
	}
	if opts.Build != "" {
		fmt.Fprintf(opts.Log, "git-red-check: %s: building without the change\n", short(commit))
		if check.Build, err = run(ctx, wt.path, opts.Build, opts.Lines); err != nil {
			return nil, err
		}
	}
	fmt.Fprintf(opts.Log, "git-red-check: %s: testing without the change\n", short(commit))
	if check.Without, err = run(ctx, wt.path, opts.Test, opts.Lines); err != nil {
		return nil, err
	}

	if _, err := git.Run("-C", wt.path, "checkout", "--quiet", "--force", "--detach", commit); err != nil {
		return nil, err
	}
	if _, err := git.Run("-C", wt.path, "clean", "-ffdxq"); err != nil {
		return nil, err
	}
	fmt.Fprintf(opts.Log, "git-red-check: %s: testing with the change\n", short(commit))
	if check.With, err = run(ctx, wt.path, opts.Test, opts.Lines); err != nil {
		return nil, err
	}

	switch {
	case check.With.Exit != 0:
		check.Verdict = FailsWithChange
	case check.Without.Exit == 0:
		check.Verdict = PassesWithoutChange
	case check.Build == nil:
		check.Verdict = FailsWithoutChange
	case check.Build.Exit != 0:
		check.Verdict = FailsToBuildWithoutChange
	default:
		check.Verdict = FailsAsAsserted
	}
	return check, nil
}

// changedFiles lists the paths that differ between from and to, limited to
// pathspecs when there are any.
func changedFiles(top, from, to string, pathspecs []string) ([]string, error) {
	args := []string{"-C", top, "diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", from, to}
	if pathspecs != nil {
		args = append(append(args, "--"), pathspecs...)
	}
	out, err := git.Bytes(nil, args...)
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

type worktree struct{ path, admin, common string }

// addWorktree checks commit out, detached, in a new directory under
// <common>/red-check.
func addWorktree(top, common, commit string) (*worktree, error) {
	parent := filepath.Join(common, "red-check")
	if err := os.MkdirAll(parent, 0o755); err != nil { //nolint:gosec // holds checkouts of the repository, in its git directory
		return nil, err
	}
	path, err := os.MkdirTemp(parent, short(commit)+"-")
	if err != nil {
		return nil, err
	}
	wt := &worktree{path: path, common: common}
	if _, err := git.Run("-C", top, "worktree", "add", "--quiet", "--detach", path, commit); err != nil {
		wt.remove(top)
		return nil, err
	}
	wt.admin, _ = git.Run("-C", path, "rev-parse", "--path-format=absolute", "--git-dir")
	return wt, nil
}

// remove deletes the worktree and git's record of it, by hand if the test
// command left it in a state `git worktree remove` refuses.
func (wt *worktree) remove(top string) {
	if _, err := git.Run("-C", top, "worktree", "remove", "--force", "--force", wt.path); err != nil {
		os.RemoveAll(wt.path)
		// Only git's own record of a worktree, never where a damaged
		// worktree's .git file might point.
		if wt.admin != "" && filepath.Dir(wt.admin) == filepath.Join(wt.common, "worktrees") {
			os.RemoveAll(wt.admin)
		}
	}
	// Left in place while another check is using it.
	os.Remove(filepath.Dir(wt.path))
}

// run runs command with sh in dir, in its own process group so that
// cancelling ctx stops everything it started, and keeps the end of its
// combined output.
func run(ctx context.Context, dir, command string, lines int) (*Run, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		stopGroup(cmd.Process.Pid, 5*time.Second)
		return nil
	}
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("interrupted: %w", ctx.Err())
	}
	r := &Run{Output: tail(out.String(), lines)}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		r.Exit = exit.ExitCode()
	case err != nil:
		return nil, err
	}
	return r, nil
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

// tail is the last n lines of s.
func tail(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
