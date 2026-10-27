package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/record"
)

// The guide's walkthrough is kept as the record's, each chunk with the
// hunks of the diff it cites; a citation of no hunk, a chunk citing none
// and a hunk no chunk cites go to the round's Dropped.
func TestGuide(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "none")
	os.WriteFile(filepath.Join(f.repo, "other.txt"), []byte("x\n"), 0o644)
	os.WriteFile(filepath.Join(f.repo, "gone.txt"), []byte("y\n"), 0o644)
	run(t, f.repo, "git", "add", "other.txt", "gone.txt")
	run(t, f.repo, "git", "commit", "-q", "-m", "Add two files")
	mid := run(t, f.repo, "git", "rev-parse", "HEAD")
	run(t, f.repo, "git", "rm", "-q", "gone.txt")
	run(t, f.repo, "git", "commit", "-q", "-m", "Remove one")
	tip := run(t, f.repo, "git", "rev-parse", "HEAD")
	f.claudeSays(t, "review-guide", claudeResult(map[string]any{"chunks": []map[string]any{
		{"title": "Grow the code", "summary": "Two more lines.",
			"hunks": []string{"file:code.txt::2@" + tip[:12], "file:nowhere.txt::1@" + tip[:12], "file:code.txt::3@" + tip[:12]}},
		{"title": "Nothing", "summary": "Cites a line of no hunk.", "hunks": []string{"file:code.txt::9@" + tip[:12]}},
	}, "unverified": []string{}}, "Walked."))

	code, out, stderr := runDelegate(t, "--plan", f.plan, mid+"..HEAD")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if walk := out.review("guide"); walk.Reviewer != "guide" {
		t.Fatalf("no guide review: %+v", out)
	}
	rec := must(record.ReadRange(out.Record))
	// code.txt did not change in mid..HEAD: only the removal is a hunk.
	if len(rec.Walkthrough) != 0 {
		t.Errorf("a walkthrough of hunks outside the range: %+v", rec.Walkthrough)
	}

	code, out, stderr = runDelegate(t, "--plan", f.plan, f.base+"..HEAD")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	rec = must(record.ReadRange(out.Record))
	if len(rec.Walkthrough) != 1 || rec.Walkthrough[0].ID != "C1" || rec.Walkthrough[0].Title != "Grow the code" ||
		len(rec.Walkthrough[0].Hunks) != 1 || rec.Walkthrough[0].Hunks[0] != (record.Hunk{Path: "code.txt", NewStart: 1,
		NewLines: 3, OldStart: 1, OldLines: 1}) {
		t.Fatalf("walkthrough %+v", rec.Walkthrough)
	}
	guide := rec.Rounds[len(rec.Rounds)-1]
	dropped := strings.Join(guide.Dropped, "\n")
	for _, want := range []string{"nowhere.txt", "cites a hunk an earlier chunk cites", "the chunk cites no hunk",
		`"path":"other.txt"`, "no chunk cites this hunk"} {
		if !strings.Contains(dropped, want) {
			t.Errorf("dropped lacks %q:\n%s", want, dropped)
		}
	}
	if guide.Reviewer != "guide" || guide.RedCheck != "absent" || len(rec.Findings) != 0 {
		t.Errorf("round %+v, findings %+v", guide, rec.Findings)
	}
	prompt := claudeArgs(t, f, "review-guide", 2)
	if text := prompt[len(prompt)-1]; !strings.HasPrefix(text, guideContract) || !strings.Contains(text, "Grow the code to three lines.") {
		t.Errorf("the guide's prompt:\n%s", text)
	}
}

// A path git quotes, and a removed line that starts like a file header,
// are read as the diff means them, kept in the record, and shown whole.
func TestGuideOddPaths(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "none")
	odd := "odd \"näme\"\tx.txt"
	os.WriteFile(filepath.Join(f.repo, odd), []byte("-- a\nb\n"), 0o644)
	run(t, f.repo, "git", "add", "--", odd)
	run(t, f.repo, "git", "commit", "-q", "-m", "Add an odd file")
	base := run(t, f.repo, "git", "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(f.repo, odd), []byte("b\n"), 0o644)
	run(t, f.repo, "git", "commit", "-q", "-am", "Drop its first line")
	r := must(resolve("", base+"..HEAD"))
	hunks := must(diffHunks(r))
	if len(hunks) != 1 || hunks[0].Path != odd || hunks[0].OldStart != 1 || hunks[0].OldLines != 2 {
		t.Fatalf("hunks %+v", hunks)
	}
	f.claudeSays(t, "review-guide", claudeResult(map[string]any{"chunks": []map[string]any{
		{"title": "Drop a line", "summary": "s", "hunks": []string{"file:" + odd + "::1@" + r.tip[:12]}}},
		"unverified": []string{}}, ""))
	code, out, stderr := runDelegate(t, "--plan", f.plan, base+"..HEAD")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	rec := must(record.ReadRange(out.Record))
	if len(rec.Walkthrough) != 1 || rec.Walkthrough[0].Hunks[0].Path != odd {
		t.Fatalf("walkthrough %+v", rec.Walkthrough)
	}
	_, stdout, _ := delegateCmd(t, "show", base+"..HEAD")
	var doc ShowDoc
	must(0, json.Unmarshal([]byte(stdout), &doc))
	if doc.Range == nil || doc.Range.Walkthrough[0].Hunks[0].Path != odd {
		t.Errorf("show: %s", stdout)
	}
}

// A hunk that only removes lines is cited at BASE.
func TestGuideCitesRemovals(t *testing.T) {
	f := setUp(t, false)
	os.WriteFile(filepath.Join(f.repo, "gone.txt"), []byte("y\nz\n"), 0o644)
	run(t, f.repo, "git", "add", "gone.txt")
	run(t, f.repo, "git", "commit", "-q", "-m", "Add a file")
	base := run(t, f.repo, "git", "rev-parse", "HEAD")
	run(t, f.repo, "git", "rm", "-q", "gone.txt")
	run(t, f.repo, "git", "commit", "-q", "-m", "Remove it")
	r := must(resolve("", base+"..HEAD"))
	out := must(validateGuide(`{"chunks": [{"title": "Remove gone.txt", "summary": "s", "hunks": ["file:gone.txt::2@`+
		base[:12]+`"]}], "unverified": []}`, r))
	if len(out.Walkthrough) != 1 || len(out.Dropped) != 0 || out.Walkthrough[0].Hunks[0].OldLines != 2 {
		t.Errorf("%+v", out)
	}
	if _, err := validateGuide(`{"chunks": [{"title": "t", "hunks": []}], "unverified": []}`, r); err == nil {
		t.Error("a chunk without a summary was taken")
	}
}
