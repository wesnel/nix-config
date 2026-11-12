package export

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

func TestMirror(t *testing.T) {
	t.Run("parent-only distribution is complete", func(t *testing.T) {
		c, p := mirrorFixture(t)
		for _, title := range []string{"Ready part", "Dependent part", "Scheduled part", "Completed part", "Abandoned part"} {
			if _, err := p.AddEntry(plans.EntryOptions{Section: "Distribution", Title: title, Text: "PRIVATE-part-body", Repo: "private/repository", Branch: "main..private-branch", Worktree: "/PRIVATE-worktree"}, day); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := p.SetAfter("P2", []string{"P1"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Postpone("P3", "2026-12-01", day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Log(plans.Event{Kind: "done", Item: "P4", Summary: "Complete"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Log(plans.Event{Kind: "abandoned", Item: "P5", Summary: "Withdrawn"}, day); err != nil {
			t.Fatal(err)
		}
		for _, full := range []bool{false, true} {
			arg := ""
			if full {
				arg = p.File()
			}
			data, err := Mirror(c, arg, day)
			if err != nil {
				t.Fatal(err)
			}
			html := string(data)
			for _, want := range []string{"Ready part", "Dependent part", "Scheduled part", "After: P1", "Scheduled: 2026-12-01", "TODO · ready", "TODO · not ready"} {
				if !strings.Contains(html, want) {
					t.Errorf("full=%v: missing %q", full, want)
				}
			}
			count := "Distribution (3)"
			if full {
				count = "Distribution (5)"
			}
			if !strings.Contains(html, count) {
				t.Errorf("full=%v: wrong part count", full)
			}
			for _, title := range []string{"Completed part", "Abandoned part"} {
				if strings.Contains(html, title) != full {
					t.Errorf("full=%v: wrong visibility of %q", full, title)
				}
			}
			for _, hidden := range []string{"PRIVATE", "private/repository", "private-branch"} {
				if strings.Contains(html, hidden) {
					t.Errorf("full=%v: leaked %q", full, hidden)
				}
			}
		}
	})
	t.Run("revision tracks content and readiness rather than collection", func(t *testing.T) {
		c, p := mirrorFixture(t)
		if _, err := p.AddEntry(plans.EntryOptions{Section: "Steps", Title: "Later work"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Postpone("S1", "2026-12-01", day); err != nil {
			t.Fatal(err)
		}
		first, err := Mirror(c, p.File(), day)
		if err != nil {
			t.Fatal(err)
		}
		later, err := Mirror(c, p.File(), day.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if MirrorRevision(first) == "" || MirrorRevision(first) != MirrorRevision(later) {
			t.Error("collection-only change changed revision")
		}
		ready, err := Mirror(c, p.File(), day.AddDate(0, 2, 0))
		if err != nil {
			t.Fatal(err)
		}
		if MirrorRevision(first) == MirrorRevision(ready) {
			t.Error("readiness change did not change revision")
		}
		if MirrorRevision(first) == MirrorRevision(append(append([]byte{}, first...), '\n')) {
			t.Error("template change did not change revision")
		}
		if _, err := p.SetSection("", "Goal", "Changed source goal", day); err != nil {
			t.Fatal(err)
		}
		changed, err := Mirror(c, p.File(), day)
		if err != nil {
			t.Fatal(err)
		}
		if MirrorRevision(first) == MirrorRevision(changed) {
			t.Error("source change did not change revision")
		}
	})
	t.Run("scheduled question remains visible", func(t *testing.T) {
		c, p := mirrorFixture(t)
		under(t, p.File(), "** Questions", "*** ASK Later question\nSCHEDULED: <2026-12-01 Tue>\n:PROPERTIES:\n:CUSTOM_ID: Q1\n:END:\n")
		data, err := Mirror(c, "", day)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Later question", "not ready to answer", "Scheduled: 2026-12-01"} {
			if !strings.Contains(string(data), want) {
				t.Errorf("missing %q", want)
			}
		}
	})
	t.Run("hostile text and uncapped lists", func(t *testing.T) {
		c, p := mirrorFixture(t)
		var steps strings.Builder
		for i := 1; i <= 105; i++ {
			fmt.Fprintf(&steps, "*** TODO Step marker %d\n:PROPERTIES:\n:CUSTOM_ID: S%d\n:END:\nPRIVATE-body\n", i, i)
		}
		under(t, p.File(), "** Steps", steps.String())
		var questions strings.Builder
		for i := 1; i <= 35; i++ {
			fmt.Fprintf(&questions, "*** DECIDED Decision marker %d\n:PROPERTIES:\n:CUSTOM_ID: Q%d\n:ANSWER: Answer marker %d\n:END:\n", i, i, i)
		}
		for i := 36; i <= 47; i++ {
			fmt.Fprintf(&questions, "*** ASK Question marker %d\n:PROPERTIES:\n:CUSTOM_ID: Q%d\n:RECOMMENDATION: Recommended\n:END:\n", i, i)
			for j := 1; j <= 8; j++ {
				fmt.Fprintf(&questions, "- Option marker %d\n", j)
			}
		}
		under(t, p.File(), "** Questions", questions.String())
		var followups strings.Builder
		for i := 1; i <= 35; i++ {
			fmt.Fprintf(&followups, "*** TODO Followup marker %d\n:PROPERTIES:\n:CUSTOM_ID: F%d\n:END:\n", i, i)
		}
		under(t, p.File(), "** Follow-ups", followups.String())
		hostile := `Text </script><img src=x onerror=alert(1)> ` + fakeToken + "\u202e"
		if _, err := p.SetSection("", "Goal", hostile, day); err != nil {
			t.Fatal(err)
		}
		data, err := Mirror(c, p.File(), day)
		if err != nil {
			t.Fatal(err)
		}
		got := string(data)
		for _, want := range []string{"Step marker 105", "Decision marker 35", "Question marker 47", "Option marker 8", "Followup marker 35", "[REDACTED GitHub token]", "&lt;img"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q", want)
			}
		}
		for _, bad := range []string{"<img", "</script>", fakeToken, "\u202e", "PRIVATE-body"} {
			if strings.Contains(got, bad) {
				t.Errorf("unsafe text %q", bad)
			}
		}
		if !strings.Contains(html.UnescapeString(got), `Text </script><img src=x onerror=alert(1)>`) {
			t.Error("hostile text was lost rather than escaped")
		}
	})
	t.Run("oversize fails without a partial result", func(t *testing.T) {
		c, p := mirrorFixture(t)
		var steps strings.Builder
		for i := 1; i <= 1100; i++ {
			fmt.Fprintf(&steps, "*** TODO %s %d\n:PROPERTIES:\n:CUSTOM_ID: S%d\n:END:\n", strings.Repeat("x", 200), i, i)
		}
		under(t, p.File(), "** Steps", steps.String())
		data, err := Mirror(c, "", day)
		if err == nil || !strings.Contains(err.Error(), "256 KiB") || data != nil {
			t.Fatalf("oversize: bytes=%d err=%v", len(data), err)
		}
	})
	t.Run("incomplete source fails without a partial result", func(t *testing.T) {
		c, p := mirrorFixture(t)
		if err := os.Rename(strings.TrimSuffix(p.File(), ".org")+".log.org", filepath.Join(t.TempDir(), "log")); err != nil {
			t.Fatal(err)
		}
		data, err := Mirror(c, "", day)
		if err == nil || data != nil {
			t.Fatalf("missing source: bytes=%d err=%v", len(data), err)
		}
	})
	t.Run("complete current plan and all open items", func(t *testing.T) {
		c := plans.Config{Dir: filepath.Join(t.TempDir(), "plans"), Host: "box"}
		made, err := c.New(plans.NewOptions{Title: "Current work", Slug: "mirror", Goal: "The complete goal"}, day)
		if err != nil {
			t.Fatal(err)
		}
		p, err := c.Open(made.Plan)
		if err != nil {
			t.Fatal(err)
		}
		for _, title := range []string{"Finished step", "Dependent step", "Scheduled step"} {
			if _, err := p.AddEntry(plans.EntryOptions{Section: "Steps", Title: title}, day); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := p.Log(plans.Event{Kind: "done", Item: "S1", Summary: "complete"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Log(plans.Event{Kind: "verified", Item: "S1", Summary: "checked"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.SetAfter("S2", []string{"S3"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Postpone("S3", "2026-12-01", day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Ask(plans.AskOptions{Question: "Blocked question", Recommendation: "Wait", After: []string{"S2"}}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Ask(plans.AskOptions{Question: "Settled question", Recommendation: "Yes", Answer: "Settled answer"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Followup(plans.FollowupOptions{Title: "Open followup", FoundIn: "S2"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Log(plans.Event{Kind: "handed-back", Item: "S2", Summary: "SENTINEL-summary", Command: "SENTINEL-command", Dir: "/SENTINEL-dir"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := c.RequestSign(plans.SignRequest{Plan: p.File(), Session: "session", Worktree: "/SENTINEL-worktree", Branches: []string{"SENTINEL-branch"}}, day); err != nil {
			t.Fatal(err)
		}
		other := c
		other.Host = "remote"
		foreign, err := other.New(plans.NewOptions{Title: "Halted foreign plan", Slug: "foreign", Status: "halted", Goal: "Foreign goal"}, day)
		if err != nil {
			t.Fatal(err)
		}
		fp, err := other.Open(foreign.Plan)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fp.AddEntry(plans.EntryOptions{Section: "Steps", Title: "Foreign open step"}, day); err != nil {
			t.Fatal(err)
		}
		for _, full := range []bool{true, false} {
			arg := ""
			if full {
				arg = p.File()
			}
			data, err := Mirror(c, arg, day)
			if err != nil {
				t.Fatal(err)
			}
			html := string(data)
			for _, want := range []string{"Current work", "Dependent step", "Scheduled step", "2026-12-01", "Blocked question", "Open followup", "Hand-back pending", "Signing pending", "Collected", "box", "Complete", "After: S3"} {
				if !strings.Contains(html, want) {
					t.Errorf("full=%v: missing %q", full, want)
				}
			}
			for _, absent := range []string{"SENTINEL", c.Dir, "<script", "https://", "GitHub"} {
				if strings.Contains(html, absent) {
					t.Errorf("full=%v: exposed %q", full, absent)
				}
			}
			for _, detail := range []string{"Finished step", "Settled question", "Settled answer", "The complete goal"} {
				if strings.Contains(html, detail) != full {
					t.Errorf("full=%v: wrong detail presence %q", full, detail)
				}
			}
			if strings.Contains(html, "Foreign open step") == full {
				t.Errorf("full=%v: foreign selection wrong", full)
			}
			if !full && (!strings.Contains(html, "halted") || !strings.Contains(html, "Source freshness unknown")) {
				t.Error("missing halted/source freshness")
			}
		}
	})
}

func mirrorFixture(t *testing.T) (plans.Config, plans.Plan) {
	t.Helper()
	c := plans.Config{Dir: filepath.Join(t.TempDir(), "plans"), Host: "box"}
	made, err := c.New(plans.NewOptions{Title: "Mirror fixture", Slug: "mirror", Goal: "Fixture goal"}, day)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}
