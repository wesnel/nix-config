package loop

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/loopsession"
)

// fakeSessionEnv makes the test binary a stand-in for a loop session: with
// "ignore" it ignores TERM, as a session too busy to stop would, and with
// "obey" it does not. It starts a child in its own group and one in a group
// of its own, as Claude Code starts each shell command, writes their PIDs
// and its own to the file in fakePIDsEnv, and waits.
const (
	fakeSessionEnv = "PR_LOOP_TEST_FAKE_SESSION"
	fakePIDsEnv    = "PR_LOOP_TEST_FAKE_PIDS"
)

func fakeSession(mode string) {
	if mode == "ignore" {
		signal.Ignore(syscall.SIGTERM)
	}
	same := exec.Command("sleep", "300")
	own := exec.Command("sleep", "300")
	own.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	for _, c := range []*exec.Cmd{same, own} {
		if err := c.Start(); err != nil {
			os.Exit(1)
		}
	}
	pids := fmt.Sprintf("%d %d %d", os.Getpid(), same.Process.Pid, own.Process.Pid)
	tmp := os.Getenv(fakePIDsEnv) + ".tmp"
	os.WriteFile(tmp, []byte(pids), 0o600)
	os.Rename(tmp, os.Getenv(fakePIDsEnv))
	same.Wait()
	select {}
}

// running reports whether pid is a process that has not exited, a zombie
// counting as exited.
func running(t *testing.T, pid int) bool {
	t.Helper()
	// The state comes last, as ps may leave it blank.
	out, err := exec.Command("ps", "-o", "pid=,stat=", "-p", strconv.Itoa(pid)).Output()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("ps: %v", err)
	}
	f := strings.Fields(string(out))
	return len(f) > 0 && (len(f) < 2 || !strings.HasPrefix(f[1], "Z"))
}

// stopRepo is a loop on one PR, #7 on feature, whose fixup GitHub does not
// have, with the PR's worktree under .pr-loop and the main worktree
// detached, as the loop leaves them while it runs.
func stopRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Start")
	gitIn(t, dir, "switch", "-q", "-c", "feature")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Add the feature")
	pushed := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "fixup! Add the feature")
	gitIn(t, dir, "switch", "-q", "--detach")
	gitIn(t, dir, "worktree", "add", "-q", filepath.Join(dir, ".pr-loop", "7"), "feature")
	useDir(t, dir)
	state := &State{Repo: "o/r", Order: []string{"feature"}, Began: time.Now().Add(-time.Hour),
		Branches: map[string]*Branch{"feature": {PR: 7, Base: "main", RemoteOid: pushed}}}
	if err := state.save(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// startFakeSession starts a stand-in loop session in a process group of its
// own, records it as sandbox-relay would, and returns its processes' PIDs.
func startFakeSession(t *testing.T, dir, mode string) []int {
	t.Helper()
	file := filepath.Join(t.TempDir(), "pids")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), fakeSessionEnv+"="+mode, fakePIDsEnv+"="+file)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	var pids []int
	for deadline := time.Now().Add(10 * time.Second); len(pids) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the fake session never started its children")
		}
		if data, err := os.ReadFile(file); err == nil {
			for _, f := range strings.Fields(string(data)) {
				n, _ := strconv.Atoi(f)
				pids = append(pids, n)
			}
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Cleanup(func() {
		for _, p := range pids {
			syscall.Kill(p, syscall.SIGKILL)
		}
	})
	err := loopsession.Write(loopsession.Record{PID: pids[0], PGID: pids[0], GitDir: filepath.Join(dir, ".git"), Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return pids
}

// A loop session may not end while its loop is active and this loop's
// report.md is unwritten.
func TestStopReason(t *testing.T) {
	dir := stopRepo(t)
	worktree := filepath.Join(dir, ".pr-loop", "7")
	report := filepath.Join(dir, ".git", "pr-loop", "report.md")
	for _, cwd := range []string{dir, worktree} {
		if got := StopReason(cwd); !strings.Contains(got, "report.md") || !strings.Contains(got, "pr-owner") {
			t.Errorf("no report, from %s: %q", cwd, got)
		}
	}
	if err := os.WriteFile(report, []byte("# An earlier loop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(report, old, old)
	if got := StopReason(worktree); got == "" {
		t.Error("an earlier loop's report let the stop through")
	}
	now := time.Now()
	os.Chtimes(report, now, now)
	if got := StopReason(worktree); got != "" {
		t.Errorf("this loop's report: %q", got)
	}
	os.Remove(report)
	if _, err := End(false); err != nil {
		t.Fatal(err)
	}
	if got := StopReason(dir); got != "" {
		t.Errorf("no loop: %q", got)
	}
	if got := StopReason(t.TempDir()); got != "" {
		t.Errorf("no repository: %q", got)
	}
}

func TestStop(t *testing.T) {
	grace := stopGrace
	stopGrace = 500 * time.Millisecond
	t.Cleanup(func() { stopGrace = grace })

	for _, mode := range []string{"obey", "ignore"} {
		t.Run("a session that would "+mode+" TERM", func(t *testing.T) {
			dir := stopRepo(t)
			pids := startFakeSession(t, dir, mode)
			result, err := Stop()
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range pids {
				if running(t, p) {
					t.Errorf("process %d outlived the stop", p)
				}
			}
			if got := gitIn(t, dir, "branch", "--show-current"); got != "feature" {
				t.Errorf("the main worktree is on %q, not the stack's top branch", got)
			}
			if exists(filepath.Join(dir, ".pr-loop", "7")) {
				t.Error("the PR's clean worktree was left")
			}
			if active, _ := Active(); active {
				t.Error("the loop's state outlived the stop")
			}
			if r, _ := loopsession.Read(filepath.Join(dir, ".git")); r != nil {
				t.Errorf("the session's record outlived the stop: %+v", r)
			}
			report, _ := os.ReadFile(filepath.Join(dir, ".git", "pr-loop", "report.md"))
			ended := map[string]string{"obey": "exited on TERM", "ignore": "killed"}[mode]
			for _, want := range []string{"stopped", ended, "feature (#7) has 1 commit GitHub does not have"} {
				if !strings.Contains(string(report), want) {
					t.Errorf("the report lacks %q:\n%s", want, report)
				}
			}
			if !strings.Contains(fmt.Sprint(result), "report.md") {
				t.Errorf("the result does not name the report: %v", result)
			}
		})
	}

	t.Run("a worktree with changes is kept", func(t *testing.T) {
		dir := stopRepo(t)
		os.WriteFile(filepath.Join(dir, ".pr-loop", "7", "draft.go"), []byte("package x\n"), 0o600)
		startFakeSession(t, dir, "obey")
		if _, err := Stop(); err != nil {
			t.Fatal(err)
		}
		if !exists(filepath.Join(dir, ".pr-loop", "7", "draft.go")) {
			t.Error("a worktree with uncommitted changes was removed")
		}
		if got := gitIn(t, dir, "branch", "--show-current"); got != "" {
			t.Errorf("the main worktree took %q, which the kept worktree has checked out", got)
		}
		report, _ := os.ReadFile(filepath.Join(dir, ".git", "pr-loop", "report.md"))
		for _, want := range []string{".pr-loop/7", "uncommitted changes", "detached"} {
			if !strings.Contains(string(report), want) {
				t.Errorf("the report lacks %q:\n%s", want, report)
			}
		}
	})

	// A clean tree is not enough: a worktree mid-rebase, or one whose HEAD
	// holds commits its branch does not, would lose that work if removed.
	for name, setUp := range map[string]func(t *testing.T, wt string){
		"rebase in progress": func(t *testing.T, wt string) {
			cmd := exec.Command("git", "-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false",
				"-C", wt, "rebase", "--exec", "false", "main")
			cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
			if cmd.Run() == nil {
				t.Fatal("the rebase did not stop")
			}
		},
		"commits off the branch": func(t *testing.T, wt string) {
			gitIn(t, wt, "switch", "-q", "--detach")
			gitIn(t, wt, "commit", "-q", "--allow-empty", "-m", "fixup! Add the feature")
		},
	} {
		t.Run("a worktree with "+name+" is kept", func(t *testing.T) {
			dir := stopRepo(t)
			wt := filepath.Join(dir, ".pr-loop", "7")
			setUp(t, wt)
			if dirty := gitIn(t, wt, "status", "--porcelain"); dirty != "" {
				t.Fatalf("the worktree is not clean: %s", dirty)
			}
			startFakeSession(t, dir, "obey")
			if _, err := Stop(); err != nil {
				t.Fatal(err)
			}
			if !exists(wt) {
				t.Fatal("the worktree was removed")
			}
			report, _ := os.ReadFile(filepath.Join(dir, ".git", "pr-loop", "report.md"))
			if !strings.Contains(string(report), "`.pr-loop/7` is kept") {
				t.Errorf("the report:\n%s", report)
			}
		})
	}

	// A record older than the process at its PID is a session's that is
	// gone, and the PID another process's.
	t.Run("a stale record", func(t *testing.T) {
		dir := stopRepo(t)
		pids := startFakeSession(t, dir, "obey")
		r, _ := loopsession.Read(filepath.Join(dir, ".git"))
		r.Started = time.Now().Add(-time.Hour)
		loopsession.Write(*r)
		if _, err := Stop(); err != nil {
			t.Fatal(err)
		}
		for _, p := range pids {
			if !running(t, p) {
				t.Errorf("process %d, newer than the record, was stopped", p)
			}
		}
		report, _ := os.ReadFile(filepath.Join(dir, ".git", "pr-loop", "report.md"))
		if !strings.Contains(string(report), "no longer running") {
			t.Errorf("the report:\n%s", report)
		}
	})

	// A record from the future would anchor any process; it is no
	// session's, and nothing is stopped or cleaned up.
	t.Run("a record from the future", func(t *testing.T) {
		dir := stopRepo(t)
		pids := startFakeSession(t, dir, "obey")
		r, _ := loopsession.Read(filepath.Join(dir, ".git"))
		r.Started = time.Now().Add(100 * 365 * 24 * time.Hour)
		loopsession.Write(*r)
		if _, err := Stop(); !refused(err) || !strings.Contains(err.Error(), "future") {
			t.Errorf("a record from the future: %v", err)
		}
		for _, p := range pids {
			if !running(t, p) {
				t.Errorf("process %d was stopped", p)
			}
		}
		if active, _ := Active(); !active {
			t.Error("a refused stop removed the loop's state")
		}
	})

	// With the session process gone, what it started still runs in the
	// group it led, as a shell's job does: the loop is still running.
	t.Run("the session process gone, its command running", func(t *testing.T) {
		dir := stopRepo(t)
		pids := startFakeSession(t, dir, "obey")
		syscall.Kill(pids[0], syscall.SIGKILL)
		for deadline := time.Now().Add(5 * time.Second); running(t, pids[0]); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the session process did not exit")
			}
		}
		if _, err := Stop(); err != nil {
			t.Fatal(err)
		}
		if running(t, pids[1]) {
			t.Error("the command in the session's group outlived the stop")
		}
		report, _ := os.ReadFile(filepath.Join(dir, ".git", "pr-loop", "report.md"))
		if strings.Contains(string(report), "no longer running") {
			t.Errorf("the report:\n%s", report)
		}
	})

	t.Run("no session recorded", func(t *testing.T) {
		stopRepo(t)
		_, err := Stop()
		if !refused(err) || !strings.Contains(err.Error(), "pr-loop end") {
			t.Errorf("stop without a session: %v", err)
		}
		if active, _ := Active(); !active {
			t.Error("a refused stop removed the loop's state")
		}
	})
}

// Which processes a record names: only those that started with the
// session, everything under them, and their group, never a group or PID
// another process took over since.
func TestSessionProcesses(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	p := func(pid, ppid, pgid int, started time.Time) proc { return proc{pid, ppid, pgid, started} }
	for _, c := range []struct {
		name  string
		procs []proc
		r     loopsession.Record
		want  []int
	}{
		{"a session and its group", []proc{p(100, 1, 50, at), p(101, 100, 101, at), p(102, 101, 101, at.Add(time.Hour)),
			p(103, 1, 103, at.Add(time.Hour))}, loopsession.Record{PID: 100, PGID: 101}, []int{100, 101, 102}},
		{"a PID that started long before the record", []proc{p(100, 1, 100, at.Add(-time.Hour))},
			loopsession.Record{PID: 100, PGID: 100}, []int{}},
		{"the group's leader gone, its members running", []proc{p(101, 1, 100, at), p(102, 101, 100, at.Add(time.Minute))},
			loopsession.Record{PID: 100, PGID: 100}, []int{101, 102}},
		{"the group's ID taken by a later job", []proc{p(100, 1, 50, at), p(101, 7, 101, at.Add(time.Hour)),
			p(103, 101, 101, at.Add(time.Hour))}, loopsession.Record{PID: 100, PGID: 101}, []int{100}},
		{"no group of its own", []proc{p(100, 1, 7, at), p(101, 100, 7, at), p(7, 1, 7, at), p(8, 7, 7, at)},
			loopsession.Record{PID: 100}, []int{100, 101}},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.r.Started = at
			if got := sessionProcesses(c.procs, &c.r); !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}

	// A record whose group is neither its session's own nor its command's
	// names someone else's processes.
	t.Run("a group that is not the session's", func(t *testing.T) {
		procs := []proc{p(100, 1, 50, at), p(200, 1, 200, at)}
		if err := checkRecord(procs, &loopsession.Record{PID: 100, PGID: 200, Started: at}, at); !refused(err) {
			t.Errorf("checkRecord: %v", err)
		}
		procs = []proc{p(100, 1, 50, at), p(200, 100, 200, at)}
		if err := checkRecord(procs, &loopsession.Record{PID: 100, PGID: 200, Started: at}, at); err != nil {
			t.Errorf("the session's own command's group: %v", err)
		}
	})
}

func TestParseElapsed(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"00:05":       5 * time.Second,
		"01:02:03":    time.Hour + 2*time.Minute + 3*time.Second,
		"2-00:00:01":  48*time.Hour + time.Second,
		"65-05:40:37": 65*24*time.Hour + 5*time.Hour + 40*time.Minute + 37*time.Second,
	} {
		if got, err := parseElapsed(in); err != nil || got != want {
			t.Errorf("parseElapsed(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "5", "a:b", "1:2:3:4", "x-00:01"} {
		if _, err := parseElapsed(in); err == nil {
			t.Errorf("parseElapsed(%q) accepted", in)
		}
	}
}
