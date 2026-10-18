package signpass

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/history"
)

// scripted writes a program that stands in for the key. Each run for which
// the shell condition key holds takes the next line of the file plan it
// returns: "miss" refuses as the key does at a missed touch, printing refusal,
// "hang" blocks as a locked key waiting on pinentry does, recording its
// process ID in PLAN.pids, "slow" takes a second before running then,
// "pid" records its process ID in PLAN.pid and waits a second before
// running then, and anything else, or an empty plan, runs then with the
// program's arguments.
func scripted(t *testing.T, dir, name, key, refusal, then string) (program, plan string) {
	t.Helper()
	program, plan = filepath.Join(dir, name), filepath.Join(dir, name+".plan")
	script := strings.NewReplacer("KEY", key, "PLAN", plan, "REFUSAL", refusal, "THEN", then).Replace(`#!/bin/sh
if KEY; then
  step=$(head -n 1 'PLAN' 2>/dev/null)
  tail -n +2 'PLAN' > 'PLAN.tmp' 2>/dev/null && mv 'PLAN.tmp' 'PLAN'
  case "$step" in
  miss) printf '%s\n' REFUSAL >&2; exit 255 ;;
  hang) echo $$ >> 'PLAN.pids'; exec sleep 30 ;;
  slow) sleep 1 ;;
  pid) echo $$ > 'PLAN.pid'; sleep 1 ;;
  esac
fi
THEN
`)
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return program, plan
}

func script(t *testing.T, plan string, steps ...string) {
	t.Helper()
	if err := os.WriteFile(plan, []byte(strings.Join(steps, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// withRetries sets how a pass with no terminal retries a missed touch for
// the rest of the test.
func withRetries(t *testing.T, retries int, wait, timeout time.Duration) {
	t.Helper()
	oldRetries, oldWait, oldTimeout := Retries, RetryWait, RetryTimeout
	Retries, RetryWait, RetryTimeout = retries, wait, timeout
	t.Cleanup(func() { Retries, RetryWait, RetryTimeout = oldRetries, oldWait, oldTimeout })
}

// captureStderr collects what the pass writes to standard error for the rest
// of the test; the returned function reads it so far.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = f
	t.Cleanup(func() { os.Stderr = old; f.Close() })
	return func() string {
		data, _ := os.ReadFile(f.Name())
		return string(data)
	}
}

// flakyRepo is a repository on branch one with two unsigned commits, signed
// through a signer that follows the plan it returns.
func flakyRepo(t *testing.T) (repo, plan string) {
	t.Helper()
	dir := t.TempDir()
	global := signingConfig(t, dir)
	program, plan := scripted(t, dir, "sign", `[ "$1 $2" = "-Y sign" ]`,
		`"Couldn't sign message (signer): agent refused operation"`, `exec ssh-keygen "$@"`)
	flaky := filepath.Join(dir, "flaky")
	os.WriteFile(flaky, []byte("[include]\n\tpath = "+global+"\n[gpg \"ssh\"]\n\tprogram = "+program+"\n"), 0o644)
	repo = filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "switch", "-qc", "one")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "a")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "b")
	t.Setenv("GIT_CONFIG_GLOBAL", flaky)
	t.Setenv("GIT_EDITOR", "false")
	useDir(t, repo)
	noTerminal(t)
	return repo, plan
}

// With no terminal to ask, a missed touch is retried once, after a wait; a
// miss on the retry, or a retry that hangs, stops the pass with the command
// that resumes it and a word about unlocking the key.
func TestMissedTouchWithoutTerminal(t *testing.T) {
	resume := "`" + testBinary(t) + " --resume one`"
	for _, c := range []struct {
		name  string
		steps []string
		// stops is whether the pass ends in an error.
		stops bool
	}{
		{"first signature missed once", []string{"miss"}, false},
		{"later signature missed once", []string{"ok", "miss"}, false},
		{"missed twice", []string{"miss", "miss"}, true},
		{"later signature missed twice", []string{"ok", "miss", "miss", "miss", "miss"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, plan := flakyRepo(t)
			withRetries(t, 1, 20*time.Millisecond, 500*time.Millisecond)
			stderr := captureStderr(t)
			script(t, plan, c.steps...)
			start := time.Now()
			result, err := Run("", false, false, false)
			if elapsed := time.Since(start); elapsed > 15*time.Second {
				t.Errorf("took %s: the retry is not bounded", elapsed)
			}
			out := stderr()
			if n := strings.Count(out, "touch the key now (retry "); n != 1 || !strings.Contains(out, "touch the key now (retry 1 of 1)") {
				t.Errorf("%d retry announcements in\n%s", n, out)
			}
			if !c.stops {
				if err != nil {
					t.Fatal(err)
				}
				if got := result.(map[string]any)["signed"]; got != 2 {
					t.Errorf("signed %v commits", got)
				}
				if marks, _ := git.Run("log", "--format=%G?", "main..one"); marks != "G\nG" {
					t.Errorf("signatures %q", marks)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), resume) || !strings.Contains(err.Error(), "pinentry") {
				t.Fatalf("a pass that gave up: %v", err)
			}
		})
	}
}

// A retry that hangs, as a locked key's does, or that the pass is
// interrupted or hung up in, is killed with the programs git started in its
// process group. A pick there is put back for `git rebase --continue`,
// keeping the signatures before it; the first commit is undone. Either way,
// the commands the pass prints, with the touches they need, sign every
// commit with its files, author and message.
func TestRetryKilled(t *testing.T) {
	for _, c := range []struct {
		name  string
		steps []string
		// sig is the signal the pass gets during the retry, or 0 for none,
		// when it times out.
		sig syscall.Signal
		// undone is whether the pass undoes the rebase; touches is what it
		// says carrying on needs.
		undone  bool
		touches string
	}{
		{"first signature", []string{"miss", "hang"}, 0, true, "up to 2 touches"},
		{"later pick", []string{"ok", "miss", "hang"}, 0, false, "up to 1 touch"},
		{"interrupted", []string{"ok", "miss", "hang"}, syscall.SIGINT, false, "up to 1 touch"},
		{"hung up", []string{"ok", "miss", "hang"}, syscall.SIGHUP, false, "up to 1 touch"},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo, plan := flakyRepo(t)
			timeout := 500 * time.Millisecond
			if c.sig != 0 {
				timeout = time.Minute
				go func() {
					for !exists(plan + ".pids") {
						time.Sleep(10 * time.Millisecond)
					}
					syscall.Kill(os.Getpid(), c.sig)
				}()
			}
			withRetries(t, 1, 20*time.Millisecond, timeout)
			stderr := captureStderr(t)
			script(t, plan, c.steps...)
			before := gitIn(t, repo, "log", "--format=%an %ae %at %T %B", "main..one")
			tip := gitIn(t, repo, "rev-parse", "one")
			start := time.Now()
			_, err := Run("", false, false, false)
			if elapsed := time.Since(start); elapsed > 15*time.Second {
				t.Errorf("took %s: the retry is not bounded", elapsed)
			}
			why := "did not finish"
			if c.sig != 0 {
				why = "was interrupted"
			}
			resume := "`" + testBinary(t) + " --resume one`"
			if err == nil || !strings.Contains(err.Error(), why) || !strings.Contains(err.Error(), resume) ||
				!strings.Contains(err.Error(), c.touches) {
				t.Fatalf("a killed retry: %v\n%s", err, stderr())
			}
			pids, _ := os.ReadFile(plan + ".pids")
			if len(pids) == 0 {
				t.Error("the signer never hung")
			}
			for _, f := range strings.Fields(string(pids)) {
				var pid int
				fmt.Sscan(f, &pid)
				if syscall.Kill(pid, 0) == nil {
					t.Errorf("the signer %d is still running", pid)
					syscall.Kill(pid, syscall.SIGKILL)
				}
			}
			if dirty := gitIn(t, repo, "status", "--porcelain"); dirty != "" {
				t.Errorf("the worktree is not clean:\n%s", dirty)
			}
			if c.undone {
				if exists(filepath.Join(repo, ".git", "rebase-merge")) {
					t.Error("a rebase is still in progress")
				}
				if now := gitIn(t, repo, "rev-parse", "one"); now != tip {
					t.Errorf("one moved to %s", now)
				}
			} else {
				if !strings.Contains(err.Error(), "`git rebase --continue`") {
					t.Errorf("no --continue in %v", err)
				}
				if marks, _ := git.Run("log", "--format=%G?", "main..HEAD"); marks != "G" {
					t.Errorf("the signature before the step is gone: %q", marks)
				}
				if _, err := git.RunEnv(history.NoEditor, "rebase", "--continue"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Resume("one", false, false); err != nil {
				t.Fatal(err)
			}
			if after := gitIn(t, repo, "log", "--format=%an %ae %at %T %B", "main..one"); after != before {
				t.Errorf("the commits changed:\n%s\n%s", before, after)
			}
			if marks, _ := git.Run("log", "--format=%G?", "main..one"); marks != "G\nG" {
				t.Errorf("signatures %q", marks)
			}
		})
	}
}

// After a retried step, the rest of the rebase runs in the pass's own
// process group, where the terminal can reach a signer that reads it or an
// interrupt.
func TestRetryRestInForeground(t *testing.T) {
	repo, plan := flakyRepo(t)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "c")
	withRetries(t, 1, 20*time.Millisecond, time.Minute)
	stderr := captureStderr(t)
	script(t, plan, "ok", "miss", "ok", "pid")
	group := make(chan int, 1)
	go func() {
		for {
			if data, err := os.ReadFile(plan + ".pid"); err == nil && strings.HasSuffix(string(data), "\n") {
				var pid int
				fmt.Sscan(string(data), &pid)
				pgid, _ := syscall.Getpgid(pid)
				group <- pgid
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if _, err := Run("", false, false, false); err != nil {
		t.Fatalf("%v\n%s", err, stderr())
	}
	select {
	case got := <-group:
		if want := syscall.Getpgrp(); got != want {
			t.Errorf("the signature after the retried one ran in process group %d, not %d", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Error("the signer never recorded its process ID")
	}
	if marks, _ := git.Run("log", "--format=%G?", "main..one"); marks != "G\nG\nG" {
		t.Errorf("signatures %q", marks)
	}
}

// --resume changes nothing in a rebase in progress; for one the pass left
// stopped, as it marks it (marking), it says how to carry on, here past a
// break a pass killed in a retry left after the step it retried (oneStep).
func TestResumeSaysHowToCarryOn(t *testing.T) {
	repo, plan := flakyRepo(t)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "c")
	withRetries(t, 0, 0, time.Second)
	stderr := captureStderr(t)
	script(t, plan, "ok", "miss")
	if _, err := Run("", false, false, false); err == nil {
		t.Fatal("a missed touch with no retries went through")
	}
	rm := filepath.Join(repo, ".git", "rebase-merge")
	if mark, _ := os.ReadFile(filepath.Join(rm, markerFile)); strings.TrimSpace(string(mark)) != gitIn(t, repo, "rev-parse", BackupRef+"one") {
		t.Errorf("the rebase is marked %q", mark)
	}
	var p pending
	readJSON(filepath.Join(repo, ".git", "sign-pass", "pending", "one.json"), &p)
	if p.GitPID == 0 {
		t.Error("the pass recorded no git")
	}
	if err := writeTodo(rm, slices.Insert(todoLines(rm), 1, "break")); err != nil {
		t.Fatal(err)
	}
	// While another git may be at work there, it says only that.
	lock := filepath.Join(repo, ".git", "index.lock")
	os.WriteFile(lock, nil, 0o644)
	if _, err := Resume("one", false, false); err == nil || !strings.Contains(err.Error(), "index.lock exists") {
		t.Fatalf("%v\n%s", err, stderr())
	}
	os.Remove(lock)
	todo := todoLines(rm)
	if _, err := Resume("one", false, false); err == nil || !strings.Contains(err.Error(), "`git rebase --continue` twice") ||
		!strings.Contains(err.Error(), "up to 2 touches") {
		t.Fatalf("%v\n%s", err, stderr())
	}
	if now := todoLines(rm); !slices.Equal(now, todo) {
		t.Fatalf("--resume changed the todo list from %q to %q", todo, now)
	}
	for range 2 {
		if _, err := git.RunEnv(history.NoEditor, "rebase", "--continue"); err != nil {
			t.Fatal(err)
		}
	}
	if exists(rm) {
		t.Fatal("the rebase is still in progress")
	}
	if _, err := Resume("one", false, false); err != nil {
		t.Fatalf("%v\n%s", err, stderr())
	}
	if marks, _ := git.Run("log", "--format=%G?", "main..one"); marks != "G\nG\nG" {
		t.Errorf("signatures %q", marks)
	}
}

// A pass started ignoring hangups, as under nohup, keeps ignoring them
// while it retries.
func TestRetryKeepsIgnoredHangup(t *testing.T) {
	signal.Ignore(syscall.SIGHUP)
	t.Cleanup(func() { signal.Reset(syscall.SIGHUP) })
	_, plan := flakyRepo(t)
	withRetries(t, 1, 20*time.Millisecond, time.Minute)
	stderr := captureStderr(t)
	script(t, plan, "ok", "miss", "pid")
	go func() {
		for !exists(plan + ".pid") {
			time.Sleep(10 * time.Millisecond)
		}
		syscall.Kill(os.Getpid(), syscall.SIGHUP)
	}()
	if _, err := Run("", false, false, false); err != nil {
		t.Fatalf("%v\n%s", err, stderr())
	}
	if marks, _ := git.Run("log", "--format=%G?", "main..one"); marks != "G\nG" {
		t.Errorf("signatures %q", marks)
	}
}

// --resume leaves the operator's own rebase of the branch a stopped pass
// left state for as it is, work in progress staged, whether or not it
// rebases onto the pass's start.
func TestResumeLeavesOthersRebase(t *testing.T) {
	editor := func(script string) []string {
		return []string{"GIT_EDITOR=:", `GIT_SEQUENCE_EDITOR=f() { awk '` + script + `' "$1" >"$1.t" && mv "$1.t" "$1"; }; f`}
	}
	for _, c := range []struct {
		name string
		// rebase starts the operator's rebase, which stops; work is the
		// file it then changes and stages.
		rebase func(t *testing.T) error
		work   string
	}{
		{"edit", func(t *testing.T) error {
			_, err := git.RunEnv(editor(`NR==4{sub(/^pick/,"edit")}1`), "-c", "commit.gpgsign=false", "rebase", "-i", "main")
			return err
		}, "y"},
		{"conflict at the first step", func(t *testing.T) error {
			_, err := git.RunEnv(editor(`{l[NR]=$0}END{print l[5]; for(i=1;i<=NR;i++) if(i!=5) print l[i]}`),
				"-c", "commit.gpgsign=false", "rebase", "-i", "main")
			return err
		}, "x"},
		{"conflict at a later step", func(t *testing.T) error {
			_, err := git.RunEnv(editor(`{l[NR]=$0}END{print l[1]; print l[5]; for(i=2;i<=NR;i++) if(i!=5) print l[i]}`),
				"-c", "commit.gpgsign=false", "rebase", "-i", "main")
			return err
		}, "x"},
		{"onto the pass's start", func(t *testing.T) error {
			_, err := git.RunEnv([]string{"GIT_EDITOR=:"}, "-c", "commit.gpgsign=false", "rebase", "--onto", "main", "one~3")
			return err
		}, "x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo, plan := flakyRepo(t)
			for _, m := range [][]string{{"x", "1", "Add x"}, {"y", "2", "Add y"}, {"x", "3", "Change x"}, {"z", "4", "Add z"}} {
				os.WriteFile(filepath.Join(repo, m[0]), []byte(m[1]+"\n"), 0o644)
				gitIn(t, repo, "add", m[0])
				gitIn(t, repo, "commit", "-q", "-m", m[2])
			}
			withRetries(t, 0, 0, time.Second)
			captureStderr(t)
			script(t, plan, "miss")
			if _, err := Run("", false, false, false); err == nil {
				t.Fatal("a missed touch with no retries went through")
			}
			c.rebase(t)
			rm := filepath.Join(repo, ".git", "rebase-merge")
			if !exists(rm) {
				t.Fatal("the operator's rebase did not stop")
			}
			os.WriteFile(filepath.Join(repo, c.work), []byte("the operator's\n"), 0o644)
			gitIn(t, repo, "add", c.work)
			todo, status, head := todoLines(rm), gitIn(t, repo, "status", "--porcelain"), gitIn(t, repo, "rev-parse", "HEAD")
			if _, err := Run("", true, false, false); err == nil || !strings.Contains(err.Error(), "not one this signing pass left stopped") {
				t.Fatalf("--resume on the operator's rebase: %v", err)
			}
			if now := gitIn(t, repo, "status", "--porcelain"); now != status {
				t.Errorf("the worktree changed from %q to %q", status, now)
			}
			if work, _ := os.ReadFile(filepath.Join(repo, c.work)); string(work) != "the operator's\n" {
				t.Errorf("the work in progress is gone: %q", work)
			}
			if now := todoLines(rm); !slices.Equal(now, todo) {
				t.Errorf("the todo list changed from %q to %q", todo, now)
			}
			if now := gitIn(t, repo, "rev-parse", "HEAD"); now != head {
				t.Errorf("HEAD moved from %s to %s", head, now)
			}
		})
	}
}

// A pass killed while it retried the first commit leaves its rebase where
// nothing is rewritten and that commit is neither done nor put back:
// --resume, changing nothing, says to undo it and sign from the start; and
// while a git the pass started there still runs, it says only that.
func TestResumeAfterKilledRestart(t *testing.T) {
	repo, plan := flakyRepo(t)
	withRetries(t, 0, 0, time.Second)
	stderr := captureStderr(t)
	script(t, plan, "miss")
	if _, err := Run("", false, false, false); err == nil {
		t.Fatal("a missed touch with no retries went through")
	}
	// What a kill mid-way through the restart leaves (oneStep), marked as
	// the pass marks its rebase (marking).
	breakFirst := strings.Replace(stopAfterFirst, `{print;print "break";`, `{print "break";print;`, 1)
	if _, err := git.RunEnv([]string{"GIT_EDITOR=:", "GIT_SEQUENCE_EDITOR=" + breakFirst},
		"rebase", "-q", "-i", "--force-rebase", "--gpg-sign", "main"); err != nil {
		t.Fatal(err)
	}
	rm := filepath.Join(repo, ".git", "rebase-merge")
	todo := todoLines(rm)
	os.WriteFile(filepath.Join(rm, "done"), []byte(todo[0]+"\n"), 0o644)
	writeTodo(rm, append([]string{"break"}, todo[1:]...))
	os.WriteFile(filepath.Join(rm, markerFile), []byte(gitIn(t, repo, "rev-parse", BackupRef+"one")+"\n"), 0o644)
	todo = todoLines(rm)

	// A git the pass started that outlived it.
	orphan := exec.Command("sleep", "30")
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { orphan.Process.Kill(); orphan.Wait() })
	state := filepath.Join(repo, ".git", "sign-pass", "pending", "one.json")
	var p pending
	readJSON(state, &p)
	p.GitPID = orphan.Process.Pid
	writeJSON(state, &p)
	if _, err := Resume("one", false, false); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("still be running on one's rebase: pid %d", p.GitPID)) {
		t.Fatalf("%v\n%s", err, stderr())
	}
	orphan.Process.Kill()
	orphan.Wait()

	_, err := Resume("one", false, false)
	if err == nil || !strings.Contains(err.Error(), "stopped mid-step") || !strings.Contains(err.Error(), "`git rebase --abort`") ||
		!strings.Contains(err.Error(), "up to 2 touches") {
		t.Fatalf("%v\n%s", err, stderr())
	}
	if now := todoLines(rm); !slices.Equal(now, todo) {
		t.Fatalf("--resume changed the todo list from %q to %q", todo, now)
	}
	if _, err := git.Run("rebase", "--abort"); err != nil {
		t.Fatal(err)
	}
	if _, err := Resume("one", false, false); err != nil {
		t.Fatalf("%v\n%s", err, stderr())
	}
	if marks, _ := git.Run("log", "--format=%s %G?", "main..one"); marks != "b G\na G" {
		t.Errorf("the commits: %q", marks)
	}
}

// A git killed after it made the retried step's commit, before it reached
// the break after it, has that step done already: putBack must not put it
// back, which would make its commit twice.
func TestPutBackAfterCommit(t *testing.T) {
	repo, _ := flakyRepo(t)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "c")
	env := []string{"GIT_EDITOR=:", "GIT_SEQUENCE_EDITOR=" + strings.Replace(stopAfterFirst, "NR==1", "NR==2", 1)}
	if _, err := git.RunEnv(env, "rebase", "-q", "-i", "--force-rebase", "--gpg-sign", "main"); err != nil {
		t.Fatal(err)
	}
	rm := filepath.Join(repo, ".git", "rebase-merge")
	done := todoLinesIn(filepath.Join(rm, "done"))
	if done[len(done)-1] != "break" {
		t.Fatalf("the rebase did not stop at the break: %q", done)
	}
	os.WriteFile(filepath.Join(rm, "done"), []byte(strings.Join(done[:len(done)-1], "\n")+"\n"), 0o644)
	writeTodo(rm, append([]string{"break"}, todoLines(rm)...))
	if back, err := putBack(rm); err != nil || !back {
		t.Fatalf("putBack: %v %v", back, err)
	}
	if _, err := git.RunEnv(history.NoEditor, "rebase", "--continue"); err != nil {
		t.Fatal(err)
	}
	if log, _ := git.Run("log", "--format=%s %G?", "main..one"); log != "c G\nb G\na G" {
		t.Errorf("the commits: %q", log)
	}
}

func todoLinesIn(path string) []string {
	data, _ := os.ReadFile(path)
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// A pass over several branches that stops says what the rest of its plan
// needs too, and the commands it prints sign every branch.
func TestPlanStopCountsTheRest(t *testing.T) {
	repo, plan := flakyRepo(t)
	gitIn(t, repo, "branch", "two", "one~1")
	gitIn(t, repo, "switch", "-q", "two")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "c")
	gitIn(t, repo, "switch", "-q", "one")
	withRetries(t, 1, 20*time.Millisecond, time.Minute)
	stderr := captureStderr(t)
	script(t, plan, "ok", "miss", "miss")
	_, err := Plan("", []string{"one", "two"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "up to 1 touch)") ||
		!strings.Contains(err.Error(), "; --resume also signs the plan's other branches after it, up to 1 touch more") {
		t.Fatalf("%v\n%s", err, stderr())
	}
	// --resume before the --continue names the worktree the rebase is in.
	if _, err := Resume("one", false, false); err == nil || !strings.Contains(err.Error(), "still in progress; run `git -C ") ||
		!strings.Contains(err.Error(), " rebase --continue` (touching the key when it blinks, up to 1 touch)") {
		t.Fatalf("%v\n%s", err, stderr())
	}
	if _, err := git.RunEnv(history.NoEditor, "rebase", "--continue"); err != nil {
		t.Fatal(err)
	}
	if _, err := Resume("one", false, false); err != nil {
		t.Fatalf("%v\n%s", err, stderr())
	}
	for _, b := range []string{"one", "two"} {
		if marks, _ := git.Run("log", "--format=%G?", "main.."+b); marks != "G\nG" {
			t.Errorf("%s's signatures %q", b, marks)
		}
	}
}

// A refusal anywhere in a fold undoes the rebase, where git keeps state that
// putting a step back would fold in twice, or does not put the step back at
// all. With no terminal the pass stops and says to resume, which signs it
// all; on a terminal, Enter signs it all again at once.
func TestFoldMissed(t *testing.T) {
	for _, c := range []struct {
		name string
		// folds are the commits after a and b, each a subject and a body.
		folds [][]string
		steps []string
		// signed is the log once the fold is signed.
		signed string
	}{
		{"squash! inside its fold", [][]string{{"squash! a", "S1."}, {"squash! a", "S2."}},
			[]string{"ok", "miss"}, "b\nG\na\n\nS1.\n\nS2.\nG"},
		{"fixup! ending a fold with a squash!", [][]string{{"squash! a", "S1."}, {"fixup! a"}},
			[]string{"ok", "ok", "miss"}, "b\nG\na\n\nS1.\nG"},
		{"squash! ending its fold", [][]string{{"fixup! a"}, {"squash! a", "S1."}},
			[]string{"ok", "ok", "miss"}, "b\nG\na\n\nS1.\nG"},
		{"pick a fold starts with", [][]string{{"fixup! b"}}, []string{"ok", "miss"}, "b\nG\na\nG"},
	} {
		for _, terminal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, terminal %v", c.name, terminal), func(t *testing.T) {
				repo, plan := flakyRepo(t)
				for _, m := range c.folds {
					os.WriteFile(filepath.Join(repo, "x"), []byte(m[0]+strings.Join(m[1:], "")), 0o644)
					gitIn(t, repo, "add", "x")
					args := []string{"commit", "-q", "-m", m[0]}
					for _, body := range m[1:] {
						args = append(args, "-m", body)
					}
					gitIn(t, repo, args...)
				}
				if terminal {
					typed(t, "\n")
				}
				withRetries(t, 1, 20*time.Millisecond, time.Minute)
				stderr := captureStderr(t)
				script(t, plan, c.steps...)
				tip := gitIn(t, repo, "rev-parse", "one")
				_, err := Run("", false, false, false)
				want := fmt.Sprintf("up to %d touches", 2+len(c.folds))
				if terminal && !strings.Contains(stderr(), "again from the start, "+want) {
					t.Errorf("the question does not say it signs from the start:\n%s", stderr())
				}
				if !terminal {
					if err == nil || !strings.Contains(err.Error(), "in a fold") || !strings.Contains(err.Error(), want) ||
						!strings.Contains(err.Error(), "`"+testBinary(t)+" --resume one`") {
						t.Fatalf("%v\n%s", err, stderr())
					}
					if exists(filepath.Join(repo, ".git", "rebase-merge")) {
						t.Error("a rebase is still in progress")
					}
					if now := gitIn(t, repo, "rev-parse", "one"); now != tip {
						t.Errorf("one moved to %s", now)
					}
					_, err = Resume("one", false, false)
				}
				if err != nil {
					t.Fatalf("%v\n%s", err, stderr())
				}
				if log, _ := git.Run("log", "--format=%B%G?", "main..one"); log != c.signed {
					t.Errorf("the commits: %q", log)
				}
			})
		}
	}
}

// A retry's RetryTimeout bounds the signature it retries, not the rest of
// the rebase, whose touches may take as long as they take.
func TestRetryBoundsOnlyItsSignature(t *testing.T) {
	repo, plan := flakyRepo(t)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "c")
	withRetries(t, 1, 20*time.Millisecond, 500*time.Millisecond)
	stderr := captureStderr(t)
	script(t, plan, "ok", "miss", "ok", "slow")
	result, err := Run("", false, false, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr())
	}
	if got := result.(map[string]any)["signed"]; got != 3 {
		t.Errorf("signed %v commits", got)
	}
	if marks, _ := git.Run("log", "--format=%G?", "main..one"); marks != "G\nG\nG" {
		t.Errorf("signatures %q", marks)
	}
}

// A pass that is told not to retry stops at the first miss, as it does when
// it cannot wait.
func TestMissedTouchWithoutRetries(t *testing.T) {
	_, plan := flakyRepo(t)
	withRetries(t, 0, 0, time.Second)
	stderr := captureStderr(t)
	script(t, plan, "miss")
	_, err := Run("", false, false, false)
	if err == nil || strings.Contains(err.Error(), "pinentry") || strings.Contains(stderr(), "retry") {
		t.Fatalf("%v\n%s", err, stderr())
	}
}

// A push whose SSH login the key refuses is retried once as well.
func TestPushMissedTouchWithoutTerminal(t *testing.T) {
	dir, repo, hub, _, armed := mirrored(t)
	t.Setenv("GIT_CONFIG_GLOBAL", signingConfig(t, dir))
	useDir(t, repo)
	noTerminal(t)
	withRetries(t, 1, 20*time.Millisecond, 10*time.Second)
	stderr := captureStderr(t)
	arm(t, armed, 0)
	if _, err := Run("", false, true, false); err != nil {
		t.Fatalf("a refused SSH login without a terminal: %v", err)
	}
	if !strings.Contains(stderr(), "touch the key now (retry 1 of 1)") {
		t.Errorf("no retry announced in\n%s", stderr())
	}
	if got, want := gitIn(t, hub, "rev-parse", "one"), gitIn(t, repo, "rev-parse", "one"); got != want {
		t.Errorf("the hub has one at %s, want %s", got, want)
	}
}
