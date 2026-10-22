package loop

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/wesnel/agent-tools/internal/git"
)

// Branch is one registered PR branch.
type Branch struct {
	PR            int    `json:"pr"`
	Base          string `json:"base"`
	RemoteOid     string `json:"remoteOid"`
	BaselineLines int    `json:"baselineLines"`
	// The PR's own commits when the loop began. Everything the loop adds must
	// be a fixup of one of these, and the signing pass squashes back to them.
	Commits []Commit `json:"commits"`
}

// Trigger is a top-level comment the loop posted to start a bot's review.
type Trigger struct {
	CommentID int64  `json:"commentId"`
	At        string `json:"at"`
	HeadOid   string `json:"headOid"`
}

// Signing is a signing pass that has started and not yet pushed.
type Signing struct {
	Upstream string            `json:"upstream"`
	Trees    map[string]string `json:"trees"`
}

// State is the loop's record, shared by every worktree of the repository.
type State struct {
	Repo             string              `json:"repo"`
	Order            []string            `json:"order"`
	Branches         map[string]*Branch  `json:"branches"`
	Triggers         map[string]*Trigger `json:"triggers"`
	AcceptedFailures map[string][]string `json:"acceptedFailures"`
	Round            int                 `json:"round"`
	Signing          *Signing            `json:"signing,omitempty"`
}

func stateDir() (string, error) {
	common, err := git.Run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "pr-loop"), nil
}

func loadState(required bool) (*State, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, fs.ErrNotExist) {
		if required {
			return nil, refuse("no loop has been started here; run `pr-loop begin` first")
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Triggers == nil {
		state.Triggers = map[string]*Trigger{}
	}
	if state.AcceptedFailures == nil {
		state.AcceptedFailures = map[string][]string{}
	}
	return &state, nil
}

// Active reports whether a review loop has been started in this repository.
func Active() (bool, error) {
	state, err := loadState(false)
	return state != nil, err
}

func (s *State) save() error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "state-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, "state.json"))
}

// parentRef is what a branch's diff is measured against: its parent in the
// stack, or the freshest local copy of the base it targets.
func (s *State) parentRef(repo *Repo, branch string) (string, error) {
	base := s.Branches[branch].Base
	if _, ok := s.Branches[base]; ok {
		return "refs/heads/" + base, nil
	}
	return trackingRef(repo, base)
}

// prBranches maps PR number to branch.
func (s *State) prBranches() map[int]string {
	out := map[int]string{}
	for b, e := range s.Branches {
		out[e.PR] = b
	}
	return out
}
