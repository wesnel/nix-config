package loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/loopsession"
)

// stopGrace is how long Stop waits for the session's processes to exit
// after TERM, and again after KILL.
var stopGrace = 10 * time.Second

// Stop ends the loop session sandbox-relay recorded for this repository:
// it signals the session, every process under it and every process in its
// group, TERM and then KILL, and once they have exited cleans up as the
// hand-back and End do. It removes the PRs' clean worktrees, puts the main
// worktree back on the stack's top branch where it can, ends the loop, and
// writes report.md saying what is left: worktrees it kept and commits
// GitHub does not have.
func Stop() (any, error) {
	common, err := git.Run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	record, err := loopsession.Read(common)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, refuse("no loop session is recorded for this repository; claude-pr-loop and codex-pr-loop record theirs " +
			"while they run. Stop any other loop session where it runs, then run `pr-loop end`")
	}
	state, err := loadState(false)
	if err != nil {
		return nil, err
	}
	how, err := stopSession(record)
	if err != nil {
		return nil, err
	}
	if err := loopsession.Remove(common, record.PID); err != nil {
		return nil, err
	}

	var left []string
	if state != nil {
		if left, err = cleanUp(state); err != nil {
			return nil, err
		}
	}
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	report := filepath.Join(dir, "report.md")
	if err := writeStopReport(report, state, how, left); err != nil {
		return nil, err
	}
	if _, err := End(false); err != nil {
		return nil, err
	}
	return map[string]any{"stopped": how, "left": left, "report": report}, nil
}

// StopReason is why a loop session working in cwd may not end its last
// turn yet, or "": the loop is active in cwd's repository and report.md is
// missing or older than the loop. A coordinator that ends its turn while a
// sub-agent runs in the background has that sub-agent stopped once
// `claude -p` gives up waiting, with no report. It reads local files
// alone.
func StopReason(cwd string) string {
	common, err := git.Run("-C", cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(common, "pr-loop", "state.json"))
	if err != nil {
		return ""
	}
	var state State
	if json.Unmarshal(data, &state) != nil {
		return ""
	}
	report := filepath.Join(common, "pr-loop", "report.md")
	if info, err := os.Stat(report); err == nil && !info.ModTime().Before(state.Began) {
		return ""
	}
	return fmt.Sprintf("The review loop is still active here and %s is not written for it. Do not end your turn while a "+
		"pr-owner or reviewer sub-agent runs: wait for each one's report in this turn, then carry on with the review-loop "+
		"skill to its Hand-back, which writes the report. If the loop must stop, write the report saying why and what is "+
		"left, then stop.", report)
}

// proc is a process that has not exited.
type proc struct {
	pid, ppid, pgid int
	started         time.Time
}

// processes lists this host's processes that have not exited; a zombie
// has.
var processes = func() ([]proc, error) {
	// Elapsed time, unlike a start time, reads the same whatever time zone
	// ps and this process each assume.
	now := time.Now()
	// The state comes last: a ps without the privilege to read it, as
	// nixpkgs' on macOS, leaves it blank.
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,pgid=,etime=,stat=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	var procs []proc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || len(f) > 5 || (len(f) == 5 && strings.HasPrefix(f[4], "Z")) {
			continue
		}
		var p proc
		var errs [4]error
		var elapsed time.Duration
		p.pid, errs[0] = strconv.Atoi(f[0])
		p.ppid, errs[1] = strconv.Atoi(f[1])
		p.pgid, errs[2] = strconv.Atoi(f[2])
		elapsed, errs[3] = parseElapsed(f[3])
		if errors.Join(errs[:]...) != nil {
			continue
		}
		p.started = now.Add(-elapsed)
		procs = append(procs, p)
	}
	return procs, nil
}

// parseElapsed reads ps's etime, [[DD-]HH:]MM:SS.
func parseElapsed(s string) (time.Duration, error) {
	days := 0
	if d, rest, ok := strings.Cut(s, "-"); ok {
		var err error
		if days, err = strconv.Atoi(d); err != nil {
			return 0, err
		}
		s = rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("etime %q", s)
	}
	total := days * 24
	units := []int{}
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, err
		}
		units = append(units, n)
	}
	if len(units) == 2 {
		units = append([]int{0}, units...)
	}
	total = ((total+units[0])*60+units[1])*60 + units[2]
	return time.Duration(total) * time.Second, nil
}

// startSlack is how long before its record a session's processes may have
// started: the relay starts, then its command, then writes the record.
const startSlack = time.Minute

// startedWith reports whether p started as the session r records did: no
// earlier than startSlack before the record, and no later than it, give or
// take the whole seconds ps counts. A process outside that span holds a PID
// the session's process had, or never had one of the session's.
func startedWith(p proc, r *loopsession.Record) bool {
	return !p.started.Before(r.Started.Add(-startSlack)) && !p.started.After(r.Started.Add(2*time.Second))
}

// checkRecord refuses a record that cannot be a session's: one written in
// the future, which every process started before, or one whose group is
// neither its session process's own nor that of a process it started.
func checkRecord(procs []proc, r *loopsession.Record, now time.Time) error {
	if r.Started.After(now.Add(2 * time.Second)) {
		return refuse("the session record says it was written at %s, in the future, so no process can be told to be "+
			"the session's; nothing was stopped. Stop the session where it runs, then delete %s",
			r.Started.Format(time.RFC3339), loopsession.Path(r.GitDir))
	}
	byPID := map[int]proc{}
	for _, p := range procs {
		byPID[p.pid] = p
	}
	session, alive := byPID[r.PID]
	leader, led := byPID[r.PGID]
	if alive && led && startedWith(session, r) && startedWith(leader, r) && r.PGID != r.PID && leader.ppid != r.PID {
		return refuse("the session record names process group %d, which is neither its session process %d's own nor "+
			"its command's; nothing was stopped. Stop the session where it runs, then delete %s",
			r.PGID, r.PID, loopsession.Path(r.GitDir))
	}
	return nil
}

// sessionProcesses are the record's processes among procs: the session
// process and everything under it, and, where the record names a group,
// everything in that group and under its members. Claude Code starts each
// shell command in a group of its own, so the group alone misses them; the
// session process may be gone while what it started runs on in its group.
// Only processes that started with the session (startedWith) anchor either:
// a group counts if one of its members did, or if the session process did
// and the group's leader is gone, never when a later process leads a group
// with the recorded ID.
func sessionProcesses(procs []proc, r *loopsession.Record) []int {
	children := map[int][]int{}
	byPID := map[int]proc{}
	for _, p := range procs {
		children[p.ppid] = append(children[p.ppid], p.pid)
		byPID[p.pid] = p
	}
	set := map[int]bool{}
	var walk func(int)
	walk = func(pid int) {
		if set[pid] {
			return
		}
		set[pid] = true
		for _, c := range children[pid] {
			walk(c)
		}
	}
	session, alive := byPID[r.PID]
	sessionAnchored := alive && startedWith(session, r)
	if sessionAnchored {
		walk(r.PID)
	}
	if r.PGID != 0 {
		var members []int
		memberAnchored := false
		for _, p := range procs {
			if p.pgid == r.PGID {
				members = append(members, p.pid)
				memberAnchored = memberAnchored || startedWith(p, r)
			}
		}
		_, led := byPID[r.PGID]
		if memberAnchored || (sessionAnchored && !led) {
			for _, m := range members {
				walk(m)
			}
		}
	}
	delete(set, os.Getpid())
	out := []int{}
	for pid := range set {
		if pid > 1 {
			out = append(out, pid)
		}
	}
	slices.Sort(out)
	return out
}

// stopSession signals the record's processes, TERM and then KILL, and
// says how they ended.
func stopSession(r *loopsession.Record) (string, error) {
	procs, err := processes()
	if err != nil {
		return "", err
	}
	if err := checkRecord(procs, r, time.Now()); err != nil {
		return "", err
	}
	targets := sessionProcesses(procs, r)
	if len(targets) == 0 {
		return "it was no longer running", nil
	}
	started := map[int]time.Time{}
	for _, p := range procs {
		started[p.pid] = p.started
	}
	for _, pid := range targets {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	if survivors, err := awaitExit(targets); err != nil || len(survivors) == 0 {
		return "it exited on TERM", err
	}
	// Checked again just before the KILL: what the record names now, which
	// takes in anything started meanwhile, and the processes signalled
	// before that still run as the same processes, not a later one given
	// a freed PID.
	if procs, err = processes(); err != nil {
		return "", err
	}
	survivors := sessionProcesses(procs, r)
	for _, p := range procs {
		was, ok := started[p.pid]
		if ok && slices.Contains(targets, p.pid) && !slices.Contains(survivors, p.pid) &&
			p.started.Sub(was).Abs() <= 2*time.Second {
			survivors = append(survivors, p.pid)
		}
	}
	for _, pid := range survivors {
		syscall.Kill(pid, syscall.SIGKILL)
	}
	survivors, err = awaitExit(survivors)
	if err != nil {
		return "", err
	}
	if len(survivors) > 0 {
		return "", fmt.Errorf("processes %v of the loop session survived KILL; nothing was cleaned up", survivors)
	}
	return fmt.Sprintf("it was killed after %s without exiting on TERM", stopGrace), nil
}

// awaitExit waits up to stopGrace for pids to exit, and returns those that
// have not.
func awaitExit(pids []int) ([]int, error) {
	deadline := time.Now().Add(stopGrace)
	for {
		procs, err := processes()
		if err != nil {
			return nil, err
		}
		var alive []int
		for _, p := range procs {
			if slices.Contains(pids, p.pid) {
				alive = append(alive, p.pid)
			}
		}
		if len(alive) == 0 || time.Now().After(deadline) {
			return alive, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// cleanUp removes the PRs' clean worktrees and puts the main worktree on
// the stack's top branch, as the hand-back does, and says what is left.
func cleanUp(state *State) ([]string, error) {
	top, err := worktreeTop()
	if err != nil {
		return nil, err
	}
	var left []string
	kept := map[string]string{}
	for _, b := range state.Order {
		entry := state.Branches[b]
		path := worktreePath(top, entry.PR)
		rel := fmt.Sprintf("`.pr-loop/%d`", entry.PR)
		if exists(path) {
			if why := unsafeToRemove(path, b); why != "" {
				kept[b] = rel
				left = append(left, fmt.Sprintf("%s is kept: %s.", rel, why))
			} else if _, err := git.Run("worktree", "remove", path); err != nil {
				kept[b] = rel
				left = append(left, fmt.Sprintf("%s is kept: %v", rel, err))
			}
		}
		if entry.RemoteOid == "" {
			continue
		}
		count, err := git.Run("rev-list", "--count", entry.RemoteOid+"..refs/heads/"+b)
		if err != nil {
			left = append(left, fmt.Sprintf("%s (#%d): cannot count the commits GitHub lacks: %v", b, entry.PR, err))
		} else if n, _ := strconv.Atoi(count); n > 0 {
			left = append(left, fmt.Sprintf("%s (#%d) has %d commit%s GitHub does not have: the loop's unpushed work, "+
				"which `pr-loop sign` would not see once the loop has ended; push or sign it with `git sign-pass`.",
				b, entry.PR, n, map[bool]string{true: "", false: "s"}[n == 1]))
		}
	}
	if len(state.Order) > 0 {
		left = append(left, reattach(top, state.Order[len(state.Order)-1], kept))
	}
	return left, nil
}

// unsafeToRemove is why removing the worktree at path, which should have
// branch checked out, would lose work, or "". A clean tree is not enough: a
// rebase, merge or cherry-pick stopped midway leaves one, with its work on
// a detached HEAD that only the worktree's own HEAD and reflog name.
func unsafeToRemove(path, branch string) string {
	if dirty, err := git.Run("-C", path, "status", "--porcelain"); err != nil || dirty != "" {
		return "it has uncommitted changes"
	}
	for _, state := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG"} {
		if p, err := git.Run("-C", path, "rev-parse", "--path-format=absolute", "--git-path", state); err != nil || exists(p) {
			return "a rebase, merge, cherry-pick, revert or bisect is in progress in it"
		}
	}
	if head, err := git.Run("-C", path, "symbolic-ref", "-q", "HEAD"); err != nil || head != "refs/heads/"+branch {
		if git.OK("-C", path, "merge-base", "--is-ancestor", "HEAD", "refs/heads/"+branch) {
			return fmt.Sprintf("its HEAD is not on %s", branch)
		}
		return fmt.Sprintf("its HEAD holds commits %s does not", branch)
	}
	return ""
}

// reattach puts the main worktree at top back on branch if the loop left it
// detached, and says where it is.
func reattach(top, branch string, kept map[string]string) string {
	if current, err := git.Run("-C", top, "branch", "--show-current"); err == nil && current != "" {
		return fmt.Sprintf("The main worktree is on %s, as the loop found it.", current)
	}
	if rel, ok := kept[branch]; ok {
		return fmt.Sprintf("The main worktree is left detached: %s has %s checked out.", rel, branch)
	}
	if dirty, err := git.Run("-C", top, "status", "--porcelain"); err != nil || dirty != "" {
		return "The main worktree is left detached: it has uncommitted changes."
	}
	if _, err := git.Run("-C", top, "switch", "-q", branch); err != nil {
		return fmt.Sprintf("The main worktree is left detached: %v", err)
	}
	return fmt.Sprintf("The main worktree is back on %s.", branch)
}

// writeStopReport writes report.md, keeping below it a report this loop's
// session wrote before it was stopped.
func writeStopReport(path string, state *State, how string, left []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Review loop stopped\n\n`pr-loop stop` stopped the loop session at %s: %s. ",
		time.Now().Format(time.RFC3339), how)
	b.WriteString("The loop's state and trigger comments are removed, as `pr-loop end` removes them; " +
		"the round snapshots under `refs/pr-loop/` stay until `pr-loop end --prune`.\n")
	if len(left) > 0 {
		b.WriteString("\n## Left\n\n")
		for _, l := range left {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	if info, err := os.Stat(path); err == nil && state != nil && !state.Began.IsZero() && !info.ModTime().Before(state.Began) {
		previous, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "\n---\n\n%s", previous)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}
