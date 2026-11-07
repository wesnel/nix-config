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

// agent-plan digest writes a digest the export then reads, and writing it
// again changes nothing.
func TestDigestCommand(t *testing.T) {
	c := setup(t)
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
	// The last day of the digest's range names it, whatever its weekday.
	if code, out, errs := agentPlan(t, "", "digest", "--date", "2026-11-05", "--text-file", file); code != 0 || !strings.Contains(out, "11-05-demo.org") {
		t.Errorf("a Thursday: %d %s %s", code, out, errs)
	}
	for _, args := range [][]string{{"digest", "--text-file", file}, {"digest", "--date", "2026-11-06"}, {"digest", "--date", "11/06", "--text-file", file}} {
		if code, _, _ := agentPlan(t, "", args...); code != 2 {
			t.Errorf("%q: exit %d", args, code)
		}
	}
}

// agent-plan brag writes an approved brag item, its summary the
// operator's answer, once per pull request; brags lists it until then.
func TestBragCommand(t *testing.T) {
	c := setup(t)
	plan := newPlan(t)
	if code, out, errs := agentPlan(t, "", "ask", plan, "--kind", "brag", "--question", "Ran the loop",
		"--recommendation", "A drafted summary."); code != 0 || !strings.Contains(out, `"id": "Q1"`) {
		t.Fatalf("ask --kind brag: %d %s %s", code, out, errs)
	}
	if code, out, errs := agentPlan(t, "", "decide", plan, "Q1", "--answer", "Agents run the loop themselves."); code != 0 {
		t.Fatalf("decide: %d %s %s", code, out, errs)
	}
	pending := func() []plans.BragItem {
		t.Helper()
		code, out, errs := agentPlan(t, "", "brags")
		var items []plans.BragItem
		if code != 0 || json.Unmarshal([]byte(out), &items) != nil {
			t.Fatalf("brags: %d %s %s", code, out, errs)
		}
		return items
	}
	if items := pending(); len(items) != 1 || items[0].ID != "Q1" || items[0].Summary != "Agents run the loop themselves." {
		t.Errorf("pending before: %+v", items)
	}
	for i, changed := range []bool{true, false} {
		code, out, errs := agentPlan(t, "", "brag", plan, "Q1", "--pr", "o/r#1", "--date", "2026-11-05")
		var w plans.NotesWrite
		if code != 0 || json.Unmarshal([]byte(out), &w) != nil || w.Changed != changed {
			t.Fatalf("run %d: %d %s %s", i+1, code, out, errs)
		}
	}
	data, _ := os.ReadFile(filepath.Join(filepath.Dir(c.Dir), "brag", "2026.org"))
	if !strings.Contains(string(data), "* Ran the loop\n:PROPERTIES:\n:PR: https://github.com/o/r/pull/1\n") ||
		!strings.HasSuffix(string(data), ":END:\nAgents run the loop themselves.\n") {
		t.Errorf("the brag file holds\n%s", data)
	}
	if items := pending(); len(items) != 0 {
		t.Errorf("pending after: %+v", items)
	}
	for _, args := range [][]string{
		{"brag", plan, "Q1", "--pr", "o/r#2"},
		{"brag", plan, "--pr", "o/r#2", "--date", "2026-11-05"},
		{"brag", plan, "Q1", "--pr", "o/r#2", "--date", "2026-11-05", "--title", "Ran the loop", "--text", "Done."},
		{"brags", plan},
	} {
		if code, _, _ := agentPlan(t, "", args...); code != 2 {
			t.Errorf("%q: exit %d", args, code)
		}
	}
}
