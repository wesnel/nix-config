package relay

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/wesnel/agent-tools/internal/loopsession"
)

// In a loop session the command runs in a process group of its own,
// recorded under the state directory for `pr-loop stop` while it runs.
func TestSessionRecord(t *testing.T) {
	t.Run("record a loop session", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		t.Setenv(loopsession.Env, "1")
		main, _ := filepath.EvalSymlinks(t.TempDir())
		if out, err := exec.Command("git", "-C", main, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		gitDir := filepath.Join(main, ".git")
		out := t.TempDir()
		code := Session([]string{"--workspace", main, "--program", "sh", "--", "sh", "-c",
			`echo $$ > ` + out + `/pid; cat "$1" > ` + out + `/record`, "sh", loopsession.Path(gitDir)}, os.Stderr)
		if code != 0 {
			t.Fatalf("session exited %d", code)
		}
		var r loopsession.Record
		data, _ := os.ReadFile(filepath.Join(out, "record"))
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatalf("no record while the command ran: %v: %s", err, data)
		}
		pid, _ := os.ReadFile(filepath.Join(out, "pid"))
		if r.PID != os.Getpid() || fmt.Sprint(r.PGID) != strings.TrimSpace(string(pid)) || r.GitDir != gitDir {
			t.Errorf("record %+v; the command was %s in this process %d", r, pid, os.Getpid())
		}
		if left, _ := loopsession.Read(gitDir); left != nil {
			t.Errorf("the record outlived the session: %+v", left)
		}
	})

	// A session that leads its own process group, as a shell's job does,
	// records that group and leaves the command in it, the terminal's.
	t.Run("record a loop session that leads its group", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		t.Setenv(loopsession.Env, "1")
		leads := groupLeader
		groupLeader = func() bool { return true }
		t.Cleanup(func() { groupLeader = leads })
		main, _ := filepath.EvalSymlinks(t.TempDir())
		out := filepath.Join(t.TempDir(), "record")
		code := Session([]string{"--workspace", main, "--program", "sh", "--", "sh", "-c",
			`cat "$1" > ` + out, "sh", loopsession.Path(main)}, os.Stderr)
		if code != 0 {
			t.Fatalf("session exited %d", code)
		}
		var r loopsession.Record
		data, _ := os.ReadFile(out)
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatalf("no record while the command ran: %v: %s", err, data)
		}
		if r.PGID != syscall.Getpgrp() {
			t.Errorf("recorded group %d, not the session's %d", r.PGID, syscall.Getpgrp())
		}
	})

	// A session that does not lead its group but whose terminal it is in the
	// foreground of, as under a wrapper script, keeps the command in that
	// group, where the terminal's input and interrupt reach it, and records
	// no group, which would be the caller's too.
	t.Run("record a loop session in the terminal's foreground", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		t.Setenv(loopsession.Env, "1")
		leads, fg := groupLeader, foreground
		groupLeader, foreground = func() bool { return false }, func() bool { return true }
		t.Cleanup(func() { groupLeader, foreground = leads, fg })
		main, _ := filepath.EvalSymlinks(t.TempDir())
		out := t.TempDir()
		code := Session([]string{"--workspace", main, "--program", "sh", "--", "sh", "-c",
			`ps -o pgid= -p $$ > ` + out + `/pgid; cat "$1" > ` + out + `/record`, "sh", loopsession.Path(main)}, os.Stderr)
		if code != 0 {
			t.Fatalf("session exited %d", code)
		}
		var r loopsession.Record
		data, _ := os.ReadFile(filepath.Join(out, "record"))
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatalf("no record while the command ran: %v: %s", err, data)
		}
		pgid, _ := os.ReadFile(filepath.Join(out, "pgid"))
		if r.PGID != 0 || strings.TrimSpace(string(pgid)) != fmt.Sprint(syscall.Getpgrp()) {
			t.Errorf("recorded group %d; the command ran in group %s, this process's is %d", r.PGID, pgid, syscall.Getpgrp())
		}
	})

	// Any other session records nothing and keeps the command in its group.
	t.Run("not a loop session", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		t.Setenv(loopsession.Env, "")
		main, _ := filepath.EvalSymlinks(t.TempDir())
		out := filepath.Join(t.TempDir(), "record")
		code := Session([]string{"--workspace", main, "--program", "sh", "--", "sh", "-c",
			`test -e "$1" && echo found > ` + out + `; true`, "sh", loopsession.Path(main)}, os.Stderr)
		if code != 0 {
			t.Fatalf("session exited %d", code)
		}
		if _, err := os.Stat(out); err == nil {
			t.Error("a session that is not a loop's was recorded")
		}
	})
}
