package plans

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// clearHandoverAge is how long a /clear's hand-over waits for the session
// it starts: Claude Code runs SessionEnd and then SessionStart at once.
const clearHandoverAge = 5 * time.Minute

// clearDir holds one hand-over per directory a session was cleared in,
// under agent-tools' state directory.
func clearDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(dir) {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "agent-tools", "cleared")
}

func clearFile(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return filepath.Join(clearDir(), hex.EncodeToString(sum[:16]))
}

type clearHandover struct {
	Plan string `json:"plan"`
	At   int64  `json:"at"`
}

// SessionEndHook is Claude Code's SessionEnd hook. /clear ends the session
// and starts one with a new ID, which no plan names; on reason "clear" it
// records the plan the ending session answers for, by its directory, for
// SessionHook to hand to the next session started there.
func (c Config) SessionEndHook(hook []byte, now time.Time) error {
	var in struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(hook, &in); err != nil {
		return err
	}
	if in.Reason != "clear" || in.SessionID == "" || in.Cwd == "" {
		return nil
	}
	top, _ := checkout(in.Cwd)
	p, _, ok := c.Locate("claude:"+in.SessionID, top)
	if !ok {
		return nil
	}
	data, err := json.Marshal(clearHandover{Plan: p.File(), At: now.Unix()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(clearDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(clearFile(in.Cwd), data, 0o600)
}

// takeHandover adds session to the SESSIONS of the plan a /clear in cwd
// handed over, when one did within clearHandoverAge, and removes the
// hand-over.
func (c Config) takeHandover(session, cwd string, now time.Time) {
	file := clearFile(cwd)
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	os.Remove(file)
	var h clearHandover
	if json.Unmarshal(data, &h) != nil || now.Sub(time.Unix(h.At, 0)) > clearHandoverAge {
		return
	}
	p, err := c.Open(h.Plan)
	if err != nil {
		return
	}
	p.withLock(func(l *lockedPlan) error {
		return l.setPlanProperty("", "SESSIONS", func(values []string) []string {
			if slices.Contains(values, session) {
				return values
			}
			return append(values, session)
		}, now)
	})
}
