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
	"slices"
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
	// The commit changes no suite's test files.
	NoTestFiles = "no-test-files"
	// The commit changes only test files, the suite's or another's, so
	// there is no change to test.
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

// Suite is one set of tests: a command and the files that hold its tests.
// A commit runs each suite whose test files it changes.
type Suite struct {
	// Name says which suite a result is of; it is unique among the suites.
	Name string `json:"name"`
	// Test is the test command, run with sh at the top of the checkout.
	Test string `json:"test"`
	// Build, if set, is run where Test fails without the change, to tell a
	// build failure from a failed assertion.
	Build string `json:"build,omitempty"`
	// TestFiles are git pathspec globs (`:(glob)` magic) for test files.
	TestFiles []string `json:"testFiles"`
}

// Equal reports whether s and o are the same suite, every field alike.
func (s Suite) Equal(o Suite) bool {
	return s.Name == o.Name && s.Test == o.Test && s.Build == o.Build && slices.Equal(s.TestFiles, o.TestFiles)
}

// CheckSuites refuses suites that cannot run: none at all, or one without
// a name, a test command or test file globs, or with a name another has.
func CheckSuites(suites []Suite) error {
	if len(suites) == 0 {
		return refusal.New("no test suites are configured, so no test ran; pass `--test CMD --test-files GLOB`, or ask " +
			"the operator to set \"suites\" in the clone's agent settings, .git/info/agents.json")
	}
	seen := map[string]int{}
	for i, s := range suites {
		n := i + 1
		switch {
		case s.Name == "":
			return refusal.New("test suite %d has no name; give each suite a name of its own", n)
		case seen[s.Name] != 0:
			return refusal.New("test suite %d is named %q, as suite %d is; give each suite a name of its own", n, s.Name,
				seen[s.Name])
		case s.Test == "":
			return refusal.New("test suite %d, %q, has no test command; give it \"test\"", n, s.Name)
		case len(s.TestFiles) == 0:
			return refusal.New("test suite %d, %q, has no test file globs; give it \"testFiles\"", n, s.Name)
		case slices.Contains(s.TestFiles, ""):
			return refusal.New("test suite %d, %q, has an empty test file glob, which would match no file; remove it", n,
				s.Name)
		}
		seen[s.Name] = n
	}
	return nil
}

// Options says what to check and how.
type Options struct {
	// Dir is any directory in the repository; empty means the process's.
	Dir string
	// Suites are the tests to run, each where a commit changes its test
	// files; CheckSuites must accept them.
	Suites []Suite
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

// SuiteCheck is the outcome of one suite for one commit.
type SuiteCheck struct {
	// Suite is the suite's name.
	Suite   string `json:"suite"`
	Verdict string `json:"verdict"`
	// Why says what a verdict other than fails-as-asserted lacks.
	Why string `json:"why,omitempty"`
	// TestFiles are the suite's test files the commit changes.
	TestFiles []string `json:"testFiles"`
	// Build is the build command without the change; Without and With are
	// the test command without and with it.
	Build   *Run `json:"buildWithoutChange,omitempty"`
	Without *Run `json:"withoutChange,omitempty"`
	With    *Run `json:"withChange,omitempty"`
}

// Check is the outcome for one commit.
type Check struct {
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
	// Verdict is the worst of its suites' verdicts, the first suite's of
	// those as bad, or no-test-files where it changes no suite's test
	// files; Why is that verdict's.
	Verdict string `json:"verdict"`
	Why     string `json:"why,omitempty"`
	// Suites are the suites whose test files it changes, in the order the
	// result's suites give.
	Suites []*SuiteCheck `json:"suites"`
}

// Result is the outcome for every checked commit, oldest first.
type Result struct {
	// Suites are the suites the check was given.
	Suites  []Suite  `json:"suites"`
	Commits []*Check `json:"commits"`
}

// worse reports whether verdict calls for a worse exit status than than
// does: disproven is worse than unproven, which is worse than proven.
func worse(verdict, than string) bool {
	rank := map[int]int{ExitProven: 0, ExitUnproven: 1, ExitDisproven: 2}
	return rank[Exit(verdict)] > rank[Exit(than)]
}

// Exit is the exit status the worst of the result's verdicts calls for.
func (r *Result) Exit() int {
	verdict := FailsAsAsserted
	for _, c := range r.Commits {
		if worse(c.Verdict, verdict) {
			verdict = c.Verdict
		}
	}
	return Exit(verdict)
}

// Do checks rev: a commit, against its parent, or a range A..B, each
// non-merge commit in it against its own parent, with each suite whose test
// files it changes.
//
// Each suite of each commit is checked in its own detached worktree under the repository's
// git directory, which a sandbox that lets git create a worktree at all lets
// it write. The worktree is removed afterwards, also on error or when ctx is
// cancelled. No ref, index or file of the caller's worktrees is touched.
func Do(ctx context.Context, rev string, opts Options) (*Result, error) {
	if err := CheckSuites(opts.Suites); err != nil {
		return nil, err
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
	result := &Result{Suites: opts.Suites, Commits: []*Check{}}
	for _, c := range commits {
		check, err := checkCommit(ctx, top, common, c, opts)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", short(c), err)
		}
		result.Commits = append(result.Commits, check)
	}
	return result, nil
}

// Worst is the verdict of the worst of checks, by the exit status each
// calls for, the first of those as bad; no-test-files where there are none.
// It is a commit's verdict from its suites'.
func Worst(checks []*SuiteCheck) string {
	verdict := NoTestFiles
	for i, c := range checks {
		if i == 0 || worse(c.Verdict, verdict) {
			verdict = c.Verdict
		}
	}
	return verdict
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
	check := &Check{Commit: commit, Subject: subject, Suites: []*SuiteCheck{}}
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
	// The change is what no suite's test files cover.
	tests := make([][]string, len(opts.Suites))
	covered := map[string]bool{}
	for i, suite := range opts.Suites {
		globs := make([]string, len(suite.TestFiles))
		for j, g := range suite.TestFiles {
			globs[j] = ":(glob)" + g
		}
		if tests[i], err = changedFiles(top, parent, commit, globs); err != nil {
			return nil, err
		}
		for _, f := range tests[i] {
			covered[f] = true
		}
	}
	code := slices.ContainsFunc(changed, func(f string) bool { return !covered[f] })
	for i, suite := range opts.Suites {
		if len(tests[i]) == 0 {
			continue
		}
		sc := &SuiteCheck{Suite: suite.Name, TestFiles: tests[i], Verdict: NoCodeChange}
		if code {
			if err := runSuite(ctx, top, common, parent, commit, suite, sc, opts); err != nil {
				return nil, fmt.Errorf("suite %q: %w", suite.Name, err)
			}
		}
		sc.Why = Messages[sc.Verdict]
		check.Suites = append(check.Suites, sc)
	}
	check.Verdict = Worst(check.Suites)
	check.Why = Messages[check.Verdict]
	return check, nil
}

// runSuite runs suite on commit, whose parent is parent, without and with
// the change, and records the runs and the verdict they give in check,
// whose TestFiles are the suite's test files the commit changes.
func runSuite(ctx context.Context, top, common, parent, commit string, suite Suite, check *SuiteCheck,
	opts Options) error {
	tests := check.TestFiles
	label := short(commit) + ": " + suite.Name

	wt, err := addWorktree(top, common, parent)
	if err != nil {
		return err
	}
	defer wt.remove(top)

	// The parent with the suite's test files: what the tests see without
	// the change. Restoring a file the commit deletes deletes it.
	literal := make([]string, len(tests))
	for i, f := range tests {
		literal[i] = ":(literal)" + f
	}
	if _, err := git.RunLong(nil, append([]string{"-C", wt.path, "restore", "--source", commit, "--staged", "--worktree", "--"}, literal...)...); err != nil {
		return err
	}
	if suite.Build != "" {
		fmt.Fprintf(opts.Log, "git-red-check: %s: building without the change\n", label)
		if check.Build, err = run(ctx, wt.path, suite.Build, opts.Lines); err != nil {
			return err
		}
	}
	fmt.Fprintf(opts.Log, "git-red-check: %s: testing without the change\n", label)
	if check.Without, err = run(ctx, wt.path, suite.Test, opts.Lines); err != nil {
		return err
	}

	if _, err := git.RunLong(nil, "-C", wt.path, "checkout", "--quiet", "--force", "--detach", commit); err != nil {
		return err
	}
	if _, err := git.RunLong(nil, "-C", wt.path, "clean", "-ffdxq"); err != nil {
		return err
	}
	fmt.Fprintf(opts.Log, "git-red-check: %s: testing with the change\n", label)
	if check.With, err = run(ctx, wt.path, suite.Test, opts.Lines); err != nil {
		return err
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
	return nil
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
	if _, err := git.RunLong(nil, "-C", top, "worktree", "add", "--quiet", "--detach", path, commit); err != nil {
		wt.remove(top)
		return nil, err
	}
	wt.admin, _ = git.Run("-C", path, "rev-parse", "--path-format=absolute", "--git-dir")
	return wt, nil
}

// remove deletes the worktree and git's record of it, by hand if the test
// command left it in a state `git worktree remove` refuses.
func (wt *worktree) remove(top string) {
	if _, err := git.RunLong(nil, "-C", top, "worktree", "remove", "--force", "--force", wt.path); err != nil {
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
