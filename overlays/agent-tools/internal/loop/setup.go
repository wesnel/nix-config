package loop

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/repos"
)

// Setup is the command that prepares a fresh checkout of a repository, such
// as generating files the repository does not commit.
type Setup struct {
	// Settings is the clone's settings file it is taken from.
	Settings string `json:"settings"`
	Command  string `json:"command"`
}

// SetupCommand looks up the setup command for the clone containing the
// current directory in its agent settings (package repos). Command is
// empty when none is configured.
func SetupCommand() (*Setup, error) {
	s, err := repos.Lookup()
	if err != nil {
		return &Setup{}, err
	}
	return &Setup{Settings: s.File(), Command: s.Setup}, nil
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
		}
		fmt.Fprintf(log, "pr-loop: setup in %s: exit %d\n", dir, run.Exit)
		runs = append(runs, run)
	}
	return runs
}
