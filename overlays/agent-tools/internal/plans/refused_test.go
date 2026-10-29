package plans

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A command refused writes nothing: not when the log's seal is broken,
// which refuses its event, and not when anything else it checks after
// writing the plan file refuses.
func TestRefusedWritesNothing(t *testing.T) {
	t.Parallel()
	text := func(s string) *string { return &s }
	commands := map[string]func(p Plan) error{
		"followup": func(p Plan) error {
			_, err := p.Followup(FollowupOptions{Title: "Later", FoundIn: "S1"}, day)
			return err
		},
		"ask": func(p Plan) error {
			_, err := p.Ask(AskOptions{Question: "Keep it?", Recommendation: "yes"}, day)
			return err
		},
		"decide": func(p Plan) error {
			_, err := p.Decide("Q1", DecideOptions{Answer: "yes"}, day)
			return err
		},
		"step": func(p Plan) error {
			_, err := p.AddEntry(EntryOptions{Section: "Steps", Title: "Tag it"}, day)
			return err
		},
		"part": func(p Plan) error {
			_, err := p.AddEntry(EntryOptions{Section: "Distribution", Title: "next"}, day)
			return err
		},
		"edit": func(p Plan) error {
			_, err := p.EditEntry("S1", EditOptions{Text: text("Done when it runs.")}, day)
			return err
		},
		"after": func(p Plan) error {
			_, err := p.SetAfter("S2", []string{"S1"}, day)
			return err
		},
		"section": func(p Plan) error {
			_, err := p.SetSection("", "Goal", "Add the loop.", day)
			return err
		},
		"postpone": func(p Plan) error {
			_, err := p.Postpone("S1", "2099-01-01", day)
			return err
		},
		"set": func(p Plan) error { return p.SetProperty("", "BRANCHES", []string{"main..other"}, day) },
		"new part": func(p Plan) error {
			_, err := Config{Dir: p.Dir, Host: p.Host}.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day)
			return err
		},
	}
	for name, run := range commands {
		t.Run("a broken seal/"+name, func(t *testing.T) {
			_, p := entryPlan(t)
			os.Remove(p.file("P1"))
			plan, log := fileText(t, p.file("")), fileText(t, p.log())
			appendTo(t, p.log(), "by hand\n")
			isRefusal(t, name, run(p))
			if after := fileText(t, p.file("")); after != plan {
				t.Errorf("%s wrote the plan:\n%s", name, after)
			}
			if _, err := os.Stat(p.file("P1")); err == nil {
				t.Errorf("%s made the part's file", name)
			}
			if after := fileText(t, p.log()); after != log+"by hand\n" {
				t.Errorf("%s wrote the log", name)
			}
		})
	}

	t.Run("an event refused after the write", func(t *testing.T) {
		_, p := entryPlan(t)
		plan := fileText(t, p.file(""))
		_, err := p.Ask(AskOptions{Question: "Keep it all?", Recommendation: "yes", Role: "bogus"}, day)
		isRefusal(t, "a bogus role", err)
		if after := fileText(t, p.file("")); after != plan {
			t.Errorf("the plan changed:\n%s", after)
		}
	})

	// A file saved by someone else after the command's write is theirs: the
	// rollback leaves it.
	t.Run("a file changed after the write", func(t *testing.T) {
		_, p := entryPlan(t)
		saved := fileText(t, p.file("")) + "Saved from Emacs.\n"
		err := p.withLock(func(l *lockedPlan) error {
			if err := l.edit(p.file(""), func(lines []string) ([]string, error) {
				return append(lines[:len(lines)-1], "Added.", ""), nil
			}); err != nil {
				return err
			}
			os.WriteFile(p.file(""), []byte(saved), 0o644)
			return errors.New("refused late")
		})
		if err == nil {
			t.Fatal("no error")
		}
		if after := fileText(t, p.file("")); after != saved {
			t.Errorf("the rollback overwrote the operator's save:\n%s", after)
		}
	})
}

// A write that fails partway, here at the file size limit as a full disk
// would fail it, leaves the plan's files as they were, so the command can
// be run again: no step without its event, no torn event, and no part
// file cut short.
func TestFailedWriteRollsBack(t *testing.T) {
	t.Parallel()
	// padded is a plan whose log is longer than any file the commands
	// below write besides it, so a limit at the log's size fails only the
	// log's append.
	padded := func(t *testing.T) Plan {
		_, p := entryPlan(t)
		os.Remove(p.file("P1"))
		for len(fileText(t, p.log())) < len(fileText(t, p.file("")))+2000 {
			if _, err := p.Log(Event{Kind: "started", Item: "S1", Summary: "padding the log with a long summary line"}, day); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	unchanged := func(t *testing.T, p Plan, plan, log string) {
		t.Helper()
		if after := fileText(t, p.file("")); after != plan {
			t.Errorf("the plan changed:\n%s", after)
		}
		if after := fileText(t, p.log()); after != log {
			t.Errorf("the log changed by %q", strings.TrimPrefix(after, log))
		}
		if _, err := os.Stat(p.file("P1")); err == nil {
			t.Error("the part's file is there")
		}
		if left, _ := filepath.Glob(filepath.Join(p.Dir, ".edit-*")); len(left) > 0 {
			t.Errorf("temporary files left: %v", left)
		}
	}
	for _, c := range []struct {
		name, command string
		// limit is the file size limit, past the log's size; a negative
		// one is the limit itself.
		limit int64
	}{
		{"a step whose event writes nothing", "step", 0},
		{"a step whose event is cut short", "step", 40},
		{"a part whose event writes nothing", "part", 0},
		{"a part whose event is cut short", "part", 40},
		{"a part file cut short", "part", -50},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := padded(t)
			plan, log := fileText(t, p.file("")), fileText(t, p.log())
			limit := int64(len(log)) + c.limit
			if c.limit < 0 {
				limit = -c.limit
			}
			atFileLimit(t, p, c.command, limit)
			unchanged(t, p, plan, log)
			if err := limitCommands[c.command](p); err != nil {
				t.Fatalf("run again: %v", err)
			}
			if n := strings.Count(fileText(t, p.file("")), "Tag it"); n > 1 {
				t.Errorf("the step is there %d times", n)
			}
		})
	}
}

// limitCommands are the commands TestFailedWriteRollsBack runs at a file
// size limit.
var limitCommands = map[string]func(Plan) error{
	"step": func(p Plan) error {
		_, err := p.AddEntry(EntryOptions{Section: "Steps", Title: "Tag it"}, day)
		return err
	},
	// part has a branch, which its creation logs.
	"part": func(p Plan) error {
		_, err := Config{Dir: p.Dir, Host: p.Host}.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1", Branches: []string{"main..part"}}, day)
		return err
	},
}

// atFileLimitEnv carries TestAtFileLimit's plan, command and limit, as
// JSON, into the child process atFileLimit starts.
const atFileLimitEnv = "AGENT_PLAN_TEST_AT_FILE_LIMIT"

// atFileLimit runs command on p in a child process whose file size limit
// is limit bytes, a write past which fails with EFBIG, and fails t unless
// the command fails. The limit is the whole process's, so it runs in a
// child: in this one it would fail the testing package's own log writes
// too.
func atFileLimit(t *testing.T, p Plan, command string, limit int64) {
	t.Helper()
	spec, err := json.Marshal(map[string]any{"plan": p, "command": command, "limit": limit})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAtFileLimit$", "-test.v")
	cmd.Env = append(os.Environ(), atFileLimitEnv+"="+string(spec))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s at a limit of %d bytes: %v\n%s", command, limit, err, out)
	}
}

// TestAtFileLimit is atFileLimit's child, and passes at once without it.
func TestAtFileLimit(t *testing.T) {
	spec := os.Getenv(atFileLimitEnv)
	if spec == "" {
		return
	}
	var s struct {
		Plan    Plan   `json:"plan"`
		Command string `json:"command"`
		Limit   uint64 `json:"limit"`
	}
	if err := json.Unmarshal([]byte(spec), &s); err != nil {
		t.Fatal(err)
	}
	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: s.Limit, Max: old.Max}); err != nil {
		t.Fatal(err)
	}
	err := limitCommands[s.Command](s.Plan)
	if lerr := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); lerr != nil {
		t.Fatal(lerr)
	}
	if err == nil {
		t.Fatal("no error at the limit")
	}
	t.Logf("refused: %v", err)
}

// A temporary file a write left behind, its process killed before the
// rename, is cleared by the next write; one being written now is not.
func TestStaleEditFile(t *testing.T) {
	_, p := entryPlan(t)
	stale, fresh := filepath.Join(p.Dir, ".edit-stale"), filepath.Join(p.Dir, ".edit-fresh")
	os.WriteFile(stale, []byte("x"), 0o644)
	os.WriteFile(fresh, []byte("x"), 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(stale, old, old)
	if _, err := p.AddEntry(EntryOptions{Section: "Steps", Title: "Tag it"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("the stale temporary file is still there")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a fresh temporary file was removed")
	}
}
