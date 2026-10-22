// Package prompttime tells a session the time on every prompt, and how
// long it has been since the operator's previous message, so an agent can
// tell which of what it knows about the outside world may have moved on.
package prompttime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultDir is where Remind keeps the time of each session's last message
// from the operator, in agent-tools' state directory.
func DefaultDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(dir) {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "agent-tools", "prompts")
}

// sessionName is a session ID usable as a file name.
var sessionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Remind is the line for a prompt of a session's main thread: Claude Code's
// and Codex's UserPromptSubmit without agent_id. It gives the time now and
// how long since the operator's previous message in the session, as dir
// records it, and records now when the prompt is the operator's own, which
// a Claude Code task-notification is not. It is "" for any other event, and
// for a session ID that is no file name.
func Remind(dir string, input []byte, now time.Time) string {
	var in struct {
		Event     string `json:"hook_event_name"`
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
		Prompt    string `json:"prompt"`
	}
	if json.Unmarshal(input, &in) != nil || in.Event != "UserPromptSubmit" || in.AgentID != "" {
		return ""
	}
	session := in.SessionID
	if !sessionName.MatchString(session) {
		return ""
	}
	file := filepath.Join(dir, session)
	line := "It is " + now.Format("2006-01-02 Mon 15:04 MST")
	if data, err := os.ReadFile(file); err == nil {
		if last, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data))); err == nil {
			line += ", " + elapsed(now.Sub(last)) + " since the operator's previous message."
		}
	} else if os.IsNotExist(err) {
		line += ", at the operator's first message in this session."
	}
	if !strings.HasPrefix(in.Prompt, "<task-notification>") {
		record(file, now)
	}
	if !strings.HasSuffix(line, ".") {
		line += "."
	}
	return line
}

// record replaces file with now, by a rename, so a prompt read at the same
// moment sees the old time or the new one, never a partial file.
func record(file string, now time.Time) {
	if os.MkdirAll(filepath.Dir(file), 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".prompt-*")
	if err != nil {
		return
	}
	_, err = tmp.WriteString(now.Format(time.RFC3339) + "\n")
	if cerr := tmp.Close(); err == nil && cerr == nil && os.Rename(tmp.Name(), file) == nil {
		return
	}
	os.Remove(tmp.Name())
}

// elapsed is d in its largest unit of days, hours, minutes and seconds,
// and the next unit down where that is not zero: "2d 3h", "2h", "45s".
func elapsed(d time.Duration) string {
	units := []struct {
		size time.Duration
		name string
	}{{24 * time.Hour, "d"}, {time.Hour, "h"}, {time.Minute, "m"}, {time.Second, "s"}}
	for i, u := range units {
		if d < u.size && i < len(units)-1 {
			continue
		}
		n := max(d/u.size, 0)
		s := fmt.Sprintf("%d%s", n, u.name)
		if i < len(units)-1 {
			if r := (d - n*u.size) / units[i+1].size; r > 0 {
				s += fmt.Sprintf(" %d%s", r, units[i+1].name)
			}
		}
		return s
	}
	return ""
}
