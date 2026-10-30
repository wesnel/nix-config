package plans

import (
	"strings"
	"testing"
)

func TestCommitAssociations(t *testing.T) {
	t.Run("retry after a partial chained rewrite", func(t *testing.T) {
		c := testConfig(t)
		repo := commitRepo(t)
		p := newPlan(t, c, "partial")
		a, b, d, x, y := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), strings.Repeat("d", 40), strings.Repeat("e", 40)
		for _, e := range []Event{
			{Kind: "committed", Repository: "o/r", Revision: a, Commit: "Original"},
			{Kind: "commit-rewritten", Repository: "o/r", OldRevision: a, Revision: b},
			{Kind: "commit-rewritten", Repository: "o/r", OldRevision: b, Revision: d},
		} {
			if _, err := p.Record("", e, day); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.RecordRewrites("session", repo, "o/r", map[string]string{b: d, x: y}, day); err != nil {
			t.Fatal(err)
		}
		got, err := c.ResolveCommit("o/r", x)
		if err != nil || got.Revision != y {
			t.Fatalf("partial retry lost alias: %+v, %v", got, err)
		}
	})
	t.Run("rewrites go to the plan holding the commit", func(t *testing.T) {
		c := testConfig(t)
		repo := commitRepo(t)
		p := newPlan(t, c, "holder")
		a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
		if _, err := p.Record("", Event{Kind: "committed", Commit: "Original", Repository: "o/r", Revision: a}, day); err != nil {
			t.Fatal(err)
		}
		before, _, _ := c.Plans()
		if err := c.RecordRewrites("", repo, "o/r", map[string]string{a: b}, day); err != nil {
			t.Fatal(err)
		}
		after, _, _ := c.Plans()
		if len(after) != len(before) {
			t.Errorf("made a plan for the rewrite: %d plans, had %d", len(after), len(before))
		}
		events, _ := p.Events()
		if last := events[len(events)-1]; last.Kind != "commit-rewritten" || last.OldRevision != a || last.Revision != b {
			t.Errorf("holding plan's last event: %+v", last)
		}
	})
	t.Run("an unmoved commit does not choose the plan", func(t *testing.T) {
		a, b, d := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("d", 40)
		// Plans made on the same day sort by slug, so each order puts the
		// unmoved commit's plan first once.
		for _, slugs := range [][2]string{{"aaa-unmoved", "zzz-moved"}, {"zzz-unmoved", "aaa-moved"}} {
			c := testConfig(t)
			repo := commitRepo(t)
			unmoved, moved := newPlan(t, c, slugs[0]), newPlan(t, c, slugs[1])
			if _, err := unmoved.Record("", Event{Kind: "committed", Commit: "Kept", Repository: "o/r", Revision: a}, day); err != nil {
				t.Fatal(err)
			}
			if _, err := moved.Record("", Event{Kind: "committed", Commit: "Signed", Repository: "o/r", Revision: b}, day); err != nil {
				t.Fatal(err)
			}
			if err := c.RecordRewrites("", repo, "o/r", map[string]string{a: a, b: d}, day); err != nil {
				t.Fatal(err)
			}
			events, _ := moved.Events()
			if last := events[len(events)-1]; last.Kind != "commit-rewritten" || last.OldRevision != b {
				t.Errorf("%v: the moved commit's plan did not get its rewrite: %+v", slugs, last)
			}
		}
	})
	t.Run("host owned idempotent rewrites", func(t *testing.T) {
		c := testConfig(t)
		repo := commitRepo(t)
		remote := c
		remote.Host = "away"
		p := newPlan(t, remote, "mirrored")
		a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
		if _, err := p.Record("", Event{Kind: "committed", Commit: "Same subject", Repository: "o/r", Revision: a}, day); err != nil {
			t.Fatal(err)
		}
		before, _ := p.Events()
		for range 2 {
			if err := c.RecordRewrites("session", repo, "o/r", map[string]string{a: b}, day); err != nil {
				t.Fatal(err)
			}
		}
		after, _ := p.Events()
		if len(before) != len(after) {
			t.Fatal("mirrored plan was written")
		}
		ps, _, err := c.Plans()
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, plan := range ps {
			events, _ := plan.Events()
			for _, e := range events {
				if e.Kind == "commit-rewritten" {
					count++
					if !plan.Own {
						t.Fatal("rewrite in mirrored plan")
					}
				}
			}
		}
		if count != 1 {
			t.Fatalf("rewrite count %d, want one", count)
		}
		got, err := c.Commits(CommitOptions{Plan: p.File()})
		if err != nil || len(got) != 1 || got[0].Revision != b {
			t.Fatalf("mirrored association: %+v, %v", got, err)
		}
	})
	t.Run("rewrite chains across hosts and plans", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "local")
		otherHost := c
		otherHost.Host = "away"
		other := newPlan(t, otherHost, "remote")
		a, b, d := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("d", 40)
		if _, err := other.Record("", Event{Kind: "committed", Commit: "Same subject", Repository: "o/r", Revision: a}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Record("", Event{Kind: "commit-rewritten", Repository: "o/r", OldRevision: a, Revision: b}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Record("", Event{Kind: "commit-rewritten", Repository: "o/r", OldRevision: b, Revision: d}, day); err != nil {
			t.Fatal(err)
		}
		got, err := c.Commits(CommitOptions{Plan: other.File(), Repository: "o/r", Revision: a})
		if err != nil || len(got) != 1 || got[0].Revision != d || got[0].OriginalRevision != a {
			t.Fatalf("old revision: %+v, %v", got, err)
		}
		got, err = c.Commits(CommitOptions{Repository: "o/r", Revision: d})
		if err != nil || len(got) != 1 || got[0].Plan != other.Name {
			t.Fatalf("resolved revision: %+v, %v", got, err)
		}
		got, err = c.Commits(CommitOptions{Repository: "o/r", Revision: b})
		if err != nil || len(got) != 1 || got[0].Revision != d {
			t.Fatalf("intermediate revision: %+v, %v", got, err)
		}
	})
	t.Run("legacy and repository boundaries", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "identity")
		a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
		for _, e := range []Event{
			{Kind: "committed", Commit: "Same subject"},
			{Kind: "committed", Commit: "Same subject", Repository: "o/r", Revision: a},
			{Kind: "committed", Commit: "Same subject", Repository: "other/r", Revision: a},
			{Kind: "commit-rewritten", Repository: "o/r", OldRevision: a, Revision: b},
		} {
			if _, err := p.Record("", e, day); err != nil {
				t.Fatal(err)
			}
		}
		got, err := c.Commits(CommitOptions{})
		if err != nil || len(got) != 2 {
			t.Fatalf("associations: %+v, %v", got, err)
		}
		for _, x := range got {
			if x.Repository == "other/r" && x.Revision != a || x.Repository == "o/r" && x.Revision != b {
				t.Fatalf("repository crossed: %+v", x)
			}
		}
	})
	t.Run("invalid alias maps", func(t *testing.T) {
		for _, mode := range []string{"conflict", "cycle"} {
			t.Run(mode, func(t *testing.T) {
				c := testConfig(t)
				p := newPlan(t, c, "bad")
				a, b, d := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("d", 40)
				pairs := [][2]string{{a, b}}
				if mode == "cycle" {
					pairs = append(pairs, [2]string{b, a})
				} else {
					pairs = append(pairs, [2]string{a, d})
				}
				for _, pair := range pairs {
					if _, err := p.Record("", Event{Kind: "commit-rewritten", Repository: "o/r", OldRevision: pair[0], Revision: pair[1]}, day); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := c.Commits(CommitOptions{}); err == nil {
					t.Fatal("invalid alias map accepted")
				}
			})
		}
	})
}
