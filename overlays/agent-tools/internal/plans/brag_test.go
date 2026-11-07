package plans

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bragged asks a brag item in p, or in its part, and settles it with
// answer, the operator's summary, or leaves it open for "".
func bragged(t *testing.T, p Plan, part, title, answer string) string {
	t.Helper()
	got, err := p.Ask(AskOptions{Part: part, Question: title, Recommendation: "A drafted summary.", Kind: "brag"}, day)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "" {
		if _, err := p.Decide(got.ID, DecideOptions{Answer: answer}, day); err != nil {
			t.Fatal(err)
		}
	}
	return got.ID
}

// agentSettled writes into p a brag item an agent settled itself, as
// ask --answer wrote one before it refused to, and returns its ID.
func agentSettled(t *testing.T, p Plan) string {
	t.Helper()
	insertUnder(t, p.file(""), "** Questions", "*** DECIDED Settled by the agent :ARCHIVE:\nCLOSED: [2026-10-14 Wed 13:05]\n"+
		":PROPERTIES:\n:CUSTOM_ID: Q8\n:RECOMMENDATION: A drafted summary.\n:KIND: brag\n:ANSWER: The agent's own summary.\n"+
		":DECIDED_BY: agent\n:END:\n")
	return "Q8"
}

func TestWriteBragItem(t *testing.T) {
	merged := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)

	// The entry's title is the question's and its summary the operator's
	// answer, not the drafted recommendation; writing it again changes
	// nothing.
	t.Run("from the approved item", func(t *testing.T) {
		c, repo := notesConfig(t)
		p := newPlan(t, c, "loop")
		qid := bragged(t, p, "", "Made the loop twice as fast", "The loop takes half the time, so a run ends before lunch.")
		o := BragItemOptions{Day: merged, PR: "o/r#7", Plan: p.File(), Question: qid}
		got, err := c.WriteBragItem(o)
		if err != nil || !got.Changed {
			t.Fatalf("WriteBragItem: %+v, %v", got, err)
		}
		want := "#+title: Brag sheet 2026\n* Made the loop twice as fast\n:PROPERTIES:\n:PR: https://github.com/o/r/pull/7\n" +
			":REPOSITORY: o/r\n:PLAN: [[id:" + planID(t, p) + "][Add the loop]]\n:QUESTION: " + qid + "\n:END:\n" +
			"The loop takes half the time, so a run ends before lunch.\n"
		if data, _ := os.ReadFile(filepath.Join(repo, "brag", "2026.org")); string(data) != want {
			t.Errorf("the brag file holds\n%s\nnot\n%s", data, want)
		}
		if got, err := c.WriteBragItem(o); err != nil || got.Changed {
			t.Errorf("again: %+v, %v", got, err)
		}
	})

	// Only a decided brag item reaches the brag file: one awaiting
	// approval, dropped, of no kind or missing is refused, saying why.
	refused := map[string]struct {
		make func(t *testing.T, p Plan) string
		want string
	}{
		"awaiting approval": {func(t *testing.T, p Plan) string { return bragged(t, p, "", "Made it", "") }, "approv"},
		"dropped": {func(t *testing.T, p Plan) string {
			qid := bragged(t, p, "", "Made it", "")
			if _, err := p.Decide(qid, DecideOptions{Answer: "not worth it", Drop: true}, day); err != nil {
				t.Fatal(err)
			}
			return qid
		}, "DROPPED"},
		"a plain question": {func(t *testing.T, p Plan) string {
			got, err := p.Ask(AskOptions{Question: "Keep the flag?", Recommendation: "no", Answer: "no"}, day)
			if err != nil {
				t.Fatal(err)
			}
			return got.ID
		}, "brag"},
		"no such question":    {func(*testing.T, Plan) string { return "Q9" }, "no question Q9. Pass the ID of a brag item, as `agent-plan brags` lists it"},
		"settled by an agent": {func(t *testing.T, p Plan) string { return agentSettled(t, p) }, "operator"},
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			c, repo := notesConfig(t)
			p := newPlan(t, c, "loop")
			_, err := c.WriteBragItem(BragItemOptions{Day: merged, PR: "o/r#7", Plan: p.File(), Question: tc.make(t, p)})
			refusedSaying(t, err, tc.want)
			if _, err := os.Lstat(filepath.Join(repo, "brag")); err == nil {
				t.Errorf("a refused item made the brag directory")
			}
		})
	}
}

// PendingBrags lists the approved brag items of every host's plans, parts
// included, with their files' pull requests, until a brag file holds
// their entry; open and plain questions are not listed.
func TestPendingBrags(t *testing.T) {
	merged := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	c, _ := notesConfig(t)
	p := newPlan(t, c, "loop")
	if err := p.SetProperty("", "PULL_REQUESTS", []string{"https://github.com/o/r/pull/1"}, day); err != nil {
		t.Fatal(err)
	}
	ours := bragged(t, p, "", "Made the loop twice as fast", "The loop takes half the time.")
	bragged(t, p, "", "Awaits approval", "")
	if _, err := p.Ask(AskOptions{Question: "Keep the flag?", Recommendation: "no", Answer: "no"}, day); err != nil {
		t.Fatal(err)
	}
	agentSettled(t, p)

	mac := Config{Dir: c.Dir, Host: "mac"}
	q := newPlan(t, mac, "theirs")
	addItems(t, q.file(""), "** Distribution", "P1")
	if _, err := mac.New(NewOptions{Title: "Part", Parent: q.file(""), Part: "P1"}, day); err != nil {
		t.Fatal(err)
	}
	if err := q.SetProperty("P1", "PULL_REQUESTS", []string{"https://github.com/o/r/pull/9"}, day); err != nil {
		t.Fatal(err)
	}
	theirs := bragged(t, q, "P1", "Fixed the sync race", "Two hosts no longer lose plans.")

	shown := func(items []BragItem) string {
		var out []string
		for _, it := range items {
			out = append(out, filepath.Base(it.Plan)+" "+it.ID+" "+it.Part+" "+it.Title+" | "+it.Summary+" | "+strings.Join(it.PullRequests, ","))
		}
		return strings.Join(out, "\n")
	}
	items, err := c.PendingBrags()
	if err != nil {
		t.Fatal(err)
	}
	want := q.Name + ".org " + theirs + " P1 Fixed the sync race | Two hosts no longer lose plans. | https://github.com/o/r/pull/9\n" +
		p.Name + ".org " + ours + "  Made the loop twice as fast | The loop takes half the time. | https://github.com/o/r/pull/1"
	if got := shown(items); got != want && got != want[strings.Index(want, "\n")+1:]+"\n"+want[:strings.Index(want, "\n")] {
		t.Errorf("pending\n%s\nwant\n%s", got, want)
	}

	if _, err := c.WriteBragItem(BragItemOptions{Day: merged, PR: "o/r#1", Plan: p.File(), Question: ours}); err != nil {
		t.Fatal(err)
	}
	items, err = c.PendingBrags()
	if got := shown(items); err != nil || !strings.HasPrefix(got, q.Name+".org "+theirs+" ") || strings.Contains(got, "\n") {
		t.Errorf("after writing ours: %v\n%s", err, got)
	}
}

// An entry names the item it came from, so writing one part's item leaves
// another part's item of the same title listed.
func TestPendingBragsSameTitle(t *testing.T) {
	merged := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	c, _ := notesConfig(t)
	p := newPlan(t, c, "loop")
	addItems(t, p.file(""), "** Distribution", "P1", "P2")
	for _, part := range []string{"P1", "P2"} {
		if _, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: part}, day); err != nil {
			t.Fatal(err)
		}
	}
	first := bragged(t, p, "P1", "Made the loop faster", "The first half takes half the time.")
	second := bragged(t, p, "P2", "Made the loop faster", "The second half takes half the time.")
	if _, err := c.WriteBragItem(BragItemOptions{Day: merged, PR: "o/r#1", Plan: p.File(), Question: first}); err != nil {
		t.Fatal(err)
	}
	items, err := c.PendingBrags()
	if err != nil || len(items) != 1 || items[0].ID != second {
		t.Errorf("after writing %s: %+v, %v", first, items, err)
	}
}
