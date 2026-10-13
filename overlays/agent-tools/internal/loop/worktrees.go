package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
)

// Worktrees creates a worktree per registered PR under <top>/.pr-loop/<n>,
// inside the workspace so a sandbox that mounts only the workspace sees them.
// Commits there are unsigned through each worktree's own config, so nothing
// else in the session stops signing. With remove, it removes clean ones.
func Worktrees(remove bool) (any, error) {
	state, err := loadState(true)
	if err != nil {
		return nil, err
	}
	common, err := git.Run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	if filepath.Base(common) != ".git" {
		return nil, refuse("worktrees need a non-bare repository whose git directory is <top>/.git")
	}
	top := filepath.Dir(common)
	out := map[string]string{}

	if remove {
		for _, b := range state.Order {
			path := filepath.Join(top, ".pr-loop", fmt.Sprint(state.Branches[b].PR))
			if !exists(path) {
				continue
			}
			if err := git.CheckConfig(path); err != nil {
				return nil, err
			}
			if dirty, _ := git.Run("-C", path, "status", "--porcelain"); dirty != "" {
				return nil, refuse("%s has uncommitted changes; commit them as fixups or ask", path)
			}
			if _, err := git.Run("worktree", "remove", path); err != nil {
				return nil, err
			}
			out[b] = "removed"
		}
		return out, nil
	}

	exclude := filepath.Join(common, "info", "exclude")
	current, _ := os.ReadFile(exclude)
	if !strings.Contains(string(current), "/.pr-loop/\n") {
		if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		_, err = f.WriteString("/.pr-loop/\n")
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	if _, err := git.Run("config", "extensions.worktreeConfig", "true"); err != nil {
		return nil, err
	}
	for _, b := range state.Order {
		path := filepath.Join(top, ".pr-loop", fmt.Sprint(state.Branches[b].PR))
		if !exists(path) {
			if _, err := git.Run("worktree", "add", path, b); err != nil {
				return nil, refuse("cannot check out %s in %s (detach the main worktree first if it has it): %v", b, path, err)
			}
		}
		if _, err := git.Run("-C", path, "config", "--worktree", "commit.gpgsign", "false"); err != nil {
			return nil, err
		}
		out[b] = path
	}
	return out, nil
}
