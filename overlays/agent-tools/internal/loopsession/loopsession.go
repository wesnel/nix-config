// Package loopsession records where a review-loop session's processes
// run, for `pr-loop stop` on the host. The record lives under the user's
// state directory, outside the workspace, which the sandboxed session can
// write: a record there could point the stop at any process.
package loopsession

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Env is set to 1 in a loop session's environment by the launchers,
// claude-pr-loop and codex-pr-loop, before sandbox-relay starts it.
const Env = "PR_LOOP_SESSION"

// Record is one running loop session.
type Record struct {
	// PID is the sandbox-relay session process.
	PID int `json:"pid"`
	// PGID is the process group the session's command runs in.
	PGID    int       `json:"pgid"`
	GitDir  string    `json:"gitDir"`
	Started time.Time `json:"started"`
}

// Path is the record of the repository whose git common directory is
// gitDir, under $XDG_STATE_HOME/agent-tools/pr-loop-sessions, named by a
// hash of gitDir's real path.
func Path(gitDir string) string {
	if real, err := filepath.EvalSymlinks(gitDir); err == nil {
		gitDir = real
	}
	base := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(base) {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	sum := sha256.Sum256([]byte(filepath.Clean(gitDir)))
	return filepath.Join(base, "agent-tools", "pr-loop-sessions", hex.EncodeToString(sum[:8])+".json")
}

// Write replaces the record of r.GitDir's repository with r.
func Write(r Record) error {
	path := Path(r.GitDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "record-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Read is the record of gitDir's repository, or nil where none is.
func Read(gitDir string) (*Record, error) {
	data, err := os.ReadFile(Path(gitDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Remove removes gitDir's record if session pid wrote it, so a session
// that ends leaves a later one's record alone.
func Remove(gitDir string, pid int) error {
	r, err := Read(gitDir)
	if err != nil || r == nil || r.PID != pid {
		return err
	}
	err = os.Remove(Path(gitDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
