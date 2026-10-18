package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/usage/runs"
)

// onTerminal has usage run take every *os.File for a terminal, or none,
// until the test ends.
func onTerminal(t *testing.T, yes bool) {
	t.Helper()
	old := terminal
	terminal = func(*os.File) bool { return yes }
	t.Cleanup(func() { terminal = old })
}

// A terminal is left to the command, unread; /dev/null, a character
// device but no terminal, is not.
func TestUsageRunTerminal(t *testing.T) {
	state := setUp(t)
	t.Setenv(runs.Env, "category=loop")
	out, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var stderr bytes.Buffer

	real := terminal
	onTerminal(t, true)
	if code := run([]string{"usage", "run", "--", "sh", "-c", fakeClaude}, out, &stderr); code != 3 {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
	if got := ledger(t, state); len(got) != 0 {
		t.Errorf("read a terminal: %+v", got)
	}

	terminal = real
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if code := run([]string{"usage", "run", "--", "sh", "-c", fakeClaude}, null, &stderr); code != 3 {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
	if got := ledger(t, state); len(got) != 1 {
		t.Errorf("output to /dev/null went unrecorded: %+v", got)
	}
}

// With --pr-loop, the tag is claude-pr-loop's own: an inherited one is
// ignored, and in a review's directory the session is a review of its
// pull request, or a triage where the queue's variable says so; the
// command sees the tag it is recorded under, and not that variable.
func TestUsageRunPrLoop(t *testing.T) {
	setUp(t)
	onTerminal(t, false)
	root := filepath.Join(t.TempDir(), "pr-review")
	review := filepath.Join(root, "acme", "widget", "12", "0123456789ab")
	os.MkdirAll(review, 0o755)
	// A name pr-loop review accepts, though no plan could have it.
	underscore := filepath.Join(root, "_acme", strings.Repeat("w", 120), "7", "0123456789ab")
	os.MkdirAll(underscore, 0o755)
	// One it refuses.
	refused := filepath.Join(root, "-acme", "widget", "7", "0123456789ab")
	os.MkdirAll(refused, 0o755)
	config := filepath.Join(t.TempDir(), "pr-loop.json")
	os.WriteFile(config, []byte(`{"review": {"dir": "`+root+`"}}`), 0o644)
	t.Setenv("PR_LOOP_CONFIG", config)
	t.Setenv(runs.Env, "category=dashboard;repo=evil/forged")
	show := `printf '%s|%s\n' "$AGENT_RUN_TAG" "${AGENT_RUN_CATEGORY-unset}" >&2; ` + fakeClaude

	for _, c := range []struct {
		dir, category, want string
	}{
		{review, "triage", "category=triage;repo=acme/widget;pr=12"},
		{review, "", "category=review;repo=acme/widget;pr=12"},
		{review, "dashboard", "category=review;repo=acme/widget;pr=12"},
		{filepath.Dir(review), "triage", "category=loop"},
		{t.TempDir(), "", "category=loop"},
		{underscore, "", "category=review;repo=_acme/" + strings.Repeat("w", 120) + ";pr=7"},
		{refused, "", "category=loop"},
	} {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Chdir(c.dir)
		t.Setenv(runs.CategoryEnv, c.category)
		var stdout, stderr bytes.Buffer
		if code := run([]string{"usage", "run", "--pr-loop", "--", "sh", "-c", show}, &stdout, &stderr); code != 3 {
			t.Errorf("%s %q: exit %d: %s", c.dir, c.category, code, stderr.String())
		}
		if !bytes.Contains(stderr.Bytes(), []byte(c.want+"|unset\n")) {
			t.Errorf("%s %q: the command saw %q, want %s and no category", c.dir, c.category, stderr.String(), c.want)
		}
		r := ledger(t, state)["claude:"+hookSession]
		if want, _ := runs.ParseTag(c.want); r.Category != want.Category || r.Labels.Repo != want.Repo || r.Labels.PR != want.PR {
			t.Errorf("%s %q: recorded %+v, want %s", c.dir, c.category, r, c.want)
		}
	}
}

// waitFor waits until path exists.
func waitFor(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s never appeared", path)
}

// Off a terminal, SIGTERM, SIGHUP and SIGINT sent to usage run reach the
// command. With --pr-loop it leads a process group of its own, and they
// reach the whole group: here a shell's child, which answers by exiting 7.
// Without, it stays in this process's group, and they reach it alone.
func TestUsageRunSignals(t *testing.T) {
	setUp(t)
	onTerminal(t, false)
	t.Setenv("PR_LOOP_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	for _, c := range []struct {
		args        []string
		script      string
		ownGroup    bool
		description string
	}{
		{[]string{"--pr-loop", "--"},
			`trap : TERM HUP INT; sh -c 'trap "exit 7" TERM HUP INT; echo $$ > "$0"; while :; do sleep 0.05; done' "$0"; exit $?`,
			true, "--pr-loop"},
		{[]string{"--"}, `trap "exit 7" TERM HUP INT; echo $$ > "$0"; while :; do sleep 0.05; done`, false, "plain"},
	} {
		for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT} {
			mark := filepath.Join(t.TempDir(), "started")
			codes := make(chan int, 1)
			go func() {
				var stdout, stderr bytes.Buffer
				args := append(append([]string{"usage", "run"}, c.args...), "sh", "-c", c.script, mark)
				codes <- run(args, &stdout, &stderr)
			}()
			waitFor(t, mark)
			var pid int
			for deadline := time.Now().Add(5 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
				data, _ := os.ReadFile(mark)
				pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			}
			if group, err := syscall.Getpgid(pid); err != nil || (group != syscall.Getpgrp()) != c.ownGroup {
				t.Errorf("%s: the command's group %d (%v), this process's %d", c.description, group, err, syscall.Getpgrp())
			}
			syscall.Kill(os.Getpid(), sig)
			select {
			case code := <-codes:
				if code != 7 {
					t.Errorf("%s, %s: exit %d, want the trap's 7", c.description, sig, code)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s: %s did not reach the command", c.description, sig)
			}
		}
	}
}

// A command killed by a signal exits as a shell's would, 128 and the
// signal; one that cannot run, 126; one not found, 127.
func TestUsageRunExits(t *testing.T) {
	setUp(t)
	onTerminal(t, false)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"usage", "run", "--", "sh", "-c", "kill -TERM $$"}, &stdout, &stderr); code != 128+15 {
		t.Errorf("killed: exit %d", code)
	}
	plain := filepath.Join(t.TempDir(), "plain")
	os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o644)
	if code := run([]string{"usage", "run", "--", plain}, &stdout, &stderr); code != 126 {
		t.Errorf("not executable: exit %d", code)
	}
}

// What the command leaves running cannot hold usage run past its exit.
// It says the output was cut short, keeps the command's status, and
// records no run, whose last result may be in what was not read.
func TestUsageRunWaitDelay(t *testing.T) {
	state := setUp(t)
	onTerminal(t, false)
	t.Setenv(runs.Env, "category=loop")
	old := waitDelay
	waitDelay = 100 * time.Millisecond
	t.Cleanup(func() { waitDelay = old })
	var stdout, stderr bytes.Buffer
	start := time.Now()
	script := strings.TrimSuffix(fakeClaude, "; exit 3") + "; sleep 3 & exit 0"
	if code := run([]string{"usage", "run", "--", "sh", "-c", script}, &stdout, &stderr); code != 0 {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("waited %s for what the command left running", took)
	}
	if !strings.Contains(stderr.String(), "was not read") {
		t.Errorf("stderr %q does not say the output was cut short", stderr.String())
	}
	if got := ledger(t, state); len(got) != 0 {
		t.Errorf("recorded a run whose output was cut short: %+v", got)
	}
}
