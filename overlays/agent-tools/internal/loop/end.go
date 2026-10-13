package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
)

// End deletes the loop's trigger comments and its state. With prune it also
// drops the backup and per-round refs, which hold the unsquashed history.
func End(prune bool) (any, error) {
	state, err := loadState(false)
	if err != nil {
		return nil, err
	}
	if state != nil {
		for _, t := range state.Triggers {
			_ = gh.REST("DELETE", fmt.Sprintf("repos/%s/issues/comments/%d", state.Repo, t.CommentID), nil, nil)
		}
		dir, err := stateDir()
		if err != nil {
			return nil, err
		}
		if err := os.Remove(filepath.Join(dir, "state.json")); err != nil {
			return nil, err
		}
	}
	if prune {
		refs, err := git.Run("for-each-ref", "--format=%(refname)", "refs/pr-loop/")
		if err != nil {
			return nil, err
		}
		for _, ref := range strings.Fields(refs) {
			if _, err := git.Run("update-ref", "-d", ref); err != nil {
				return nil, err
			}
		}
	}
	return map[string]string{"ended": "loop state removed"}, nil
}
