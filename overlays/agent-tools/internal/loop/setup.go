package loop

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/repos"
)

// Setup is the command that prepares a fresh checkout of a repository, such
// as generating files the repository does not commit.
type Setup struct {
	Repo    string `json:"repo"`
	Command string `json:"command"`
	// Generated are the ignored files a fresh checkout lacks and a build
	// needs, as git pathspec globs, which `pr-loop autosquash` copies.
	Generated []string `json:"generated,omitempty"`
}

// SetupCommand looks up the setup command, and the generated files, for the
// repository containing the current directory in the user's per-repository
// settings (package repos). Command is empty when none is configured.
func SetupCommand() (*Setup, error) {
	s, err := repos.Lookup()
	if err != nil || (s.Setup == "" && len(s.Generated) == 0) {
		return &Setup{}, err
	}
	return &Setup{Repo: s.Repo, Command: s.Setup, Generated: s.Generated}, nil
}

// SetupRun is one run of a setup command in a worktree.
type SetupRun struct {
	Dir  string `json:"dir"`
	Exit int    `json:"exit"`
}

// RunSetup runs command with sh in each dir, in this process's sandbox. It
// writes the command to log before running it and its exit status after,
// and sends the command's own output there too, keeping stdout for the
// caller's JSON. Exit is -1 when the command could not be started.
func RunSetup(command string, dirs []string, log io.Writer) []SetupRun {
	runs := []SetupRun{}
	for _, dir := range dirs {
		fmt.Fprintf(log, "pr-loop: setup in %s: %s\n", dir, command)
		cmd := exec.Command("sh", "-c", command)
		cmd.Dir = dir
		cmd.Env = gitenv.Without(os.Environ())
		cmd.Stdout, cmd.Stderr = log, log
		run := SetupRun{Dir: dir}
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				run.Exit = exit.ExitCode()
			} else {
				run.Exit = -1
				fmt.Fprintf(log, "pr-loop: setup in %s: %s\n", dir, err)
			}
		} else {
			markSetUp(dir)
		}
		fmt.Fprintf(log, "pr-loop: setup in %s: exit %d\n", dir, run.Exit)
		runs = append(runs, run)
	}
	return runs
}

// setupMarker is where a worktree records the commit its last successful
// setup ran at, in its own git directory so it never shows as a change.
func setupMarker(dir string) (string, error) {
	return git.Run("-C", dir, "rev-parse", "--path-format=absolute", "--git-path", "pr-loop-setup")
}

// markSetUp records dir's HEAD as set up. Failing to record it only means
// NeedsSetup reports the worktree again.
func markSetUp(dir string) {
	head, err := git.Run("-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return
	}
	if marker, err := setupMarker(dir); err == nil {
		_ = os.WriteFile(marker, []byte(head+"\n"), 0o644)
	}
}

// NeedsSetup reports whether dir's HEAD differs from the commit its last
// successful setup ran at, which includes never having been set up.
func NeedsSetup(dir string) bool {
	head, err := git.Run("-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return true
	}
	marker, err := setupMarker(dir)
	if err != nil {
		return true
	}
	done, err := os.ReadFile(marker)
	return err != nil || strings.TrimSpace(string(done)) != head
}

// WorktreePaths lists the worktrees `Worktrees` makes for the registered
// PRs, in stack order, as this process sees them.
func WorktreePaths() ([]string, error) {
	state, err := loadState(true)
	if err != nil {
		return nil, err
	}
	top, err := worktreeTop()
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for _, b := range state.Order {
		paths = append(paths, worktreePath(top, state.Branches[b].PR))
	}
	return paths, nil
}
