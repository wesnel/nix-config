package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/export"
	"github.com/wesnel/agent-tools/internal/plans"
)

// notesSetup is setup with this host the one that writes the notes files.
func notesSetup(t *testing.T) plans.Config {
	t.Helper()
	c := setup(t)
	c.NotesHost = c.Host
	data, _ := json.Marshal(map[string]any{"plans": c})
	os.WriteFile(os.Getenv("AGENT_TOOLS_CONFIG"), data, 0o644)
	return c
}

// agent-plan digest writes a digest the export then reads, and writing it
// again changes nothing.
func TestDigestCommand(t *testing.T) {
	c := notesSetup(t)
	file := filepath.Join(t.TempDir(), "digest.org")
	os.WriteFile(file, []byte("* Add the loop\n:PROPERTIES:\n:PR: o/r#1\n:PLAN: id:abc-1\n:END:\nSHOWN-IN-THE-EXPORT\n"), 0o644)
	var w plans.NotesWrite
	for i, changed := range []bool{true, false} {
		code, out, errs := agentPlan(t, "", "digest", "--date", "2026-11-06", "--text-file", file)
		if code != 0 || json.Unmarshal([]byte(out), &w) != nil || w.Changed != changed {
			t.Fatalf("run %d: %d %s %s", i+1, code, out, errs)
		}
	}
	if w.File != filepath.Join(filepath.Dir(c.Dir), "digests", "2026", "11-06-demo.org") {
		t.Errorf("wrote %s", w.File)
	}
	today, err := export.TodayOf(c, time.Date(2026, 11, 7, 12, 0, 0, 0, time.Local))
	if err != nil || len(today.Digests) != 1 || !strings.Contains(today.Digests[0].Text, "SHOWN-IN-THE-EXPORT") || today.Date != "2026-11-06" {
		t.Errorf("the export read %+v, %v", today, err)
	}
	if code, _, errs := agentPlan(t, "", "digest", "--date", "2026-11-05", "--text-file", file); code != 2 || !strings.Contains(errs, "Thursday") {
		t.Errorf("a Thursday: %d %s", code, errs)
	}
	for _, args := range [][]string{{"digest", "--text-file", file}, {"digest", "--date", "2026-11-06"}, {"digest", "--date", "11/06", "--text-file", file}} {
		if code, _, _ := agentPlan(t, "", args...); code != 2 {
			t.Errorf("%q: exit %d", args, code)
		}
	}
}

// agent-plan brag adds one entry per pull request.
func TestBragCommand(t *testing.T) {
	c := notesSetup(t)
	plan := newPlan(t)
	for i, changed := range []bool{true, false} {
		code, out, errs := agentPlan(t, "", "brag", plan, "--pr", "o/r#1", "--date", "2026-11-05", "--title", "Ran the loop", "--text", "Done.")
		var w plans.NotesWrite
		if code != 0 || json.Unmarshal([]byte(out), &w) != nil || w.Changed != changed {
			t.Fatalf("run %d: %d %s %s", i+1, code, out, errs)
		}
	}
	data, _ := os.ReadFile(filepath.Join(filepath.Dir(c.Dir), "brag", "2026.org"))
	if strings.Count(string(data), ":PR: https://github.com/o/r/pull/1\n") != 1 || !strings.HasSuffix(string(data), ":END:\nDone.\n") {
		t.Errorf("the brag file holds\n%s", data)
	}
	if code, _, _ := agentPlan(t, "", "brag", plan, "--pr", "o/r#2", "--title", "No date", "--text", "Done."); code != 2 {
		t.Errorf("without --date: exit %d", code)
	}
}
