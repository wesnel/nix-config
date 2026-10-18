package spend

import (
	"testing"

	"github.com/wesnel/agent-tools/internal/usage/runs"
)

// A session the run ledger tags takes its category, repository and plan
// from the tag, and its sub-agents with it; a plan naming the session, and
// the dashboard's config directory, still win.
func TestRunLabels(t *testing.T) {
	f := newFixture(t)
	f.opts.Runs = map[string]runs.Record{
		sNoEntry:     {Category: runs.Triage, Labels: runs.Labels{Repo: "acme/gadget", PR: 9, Plan: "2026-10-15-y.box"}},
		sInteractive: {Category: runs.Loop},
		sScratch:     {Category: runs.Review, Labels: runs.Labels{Plan: "2026-10-15-y.box"}},
		sDashboard:   {Category: runs.Loop},
	}

	category := collect(t, f, "category")
	for label, total := range map[string]int64{"triage": 303, "dashboard": 506} {
		if got := row(t, category, label); got.Total != total {
			t.Errorf("%s total %d, want %d", label, got.Total, total)
		}
	}
	// The interactive session's sub-agents are its loop's.
	if got := row(t, category, "loop"); got.Requests != 1+5 || got.Agents != 2 {
		t.Errorf("loop requests %d agents %d, want 6 and 2", got.Requests, got.Agents)
	}
	if got := row(t, category, "review"); got.Requests != 2 {
		t.Errorf("review requests %d, want the review directory's and the tagged scratch session's", got.Requests)
	}
	for _, x := range category.Rows {
		if x.Label == Unknown {
			t.Errorf("an unknown row is left: %+v", x)
		}
	}

	repo := collect(t, f, "repo")
	if got := row(t, repo, "acme/gadget").Requests; got != 2 {
		t.Errorf("acme/gadget requests %d, want 2", got)
	}
	plan := collect(t, f, "plan")
	if got := row(t, plan, "2026-10-14-x.box").Requests; got != 1 {
		t.Errorf("the plan naming a session lost it: %d requests", got)
	}
	if got := row(t, plan, "2026-10-15-y.box").Requests; got != 1 {
		t.Errorf("the tag's plan has %d requests, want 1", got)
	}
}
