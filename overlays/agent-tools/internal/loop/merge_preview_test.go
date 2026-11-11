package loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestMergePreview(t *testing.T) {
	t.Run("GitHub states", func(t *testing.T) {
		mergeRepo(t, "")
		original := graphQL
		graphQL = func(query string, vars map[string]any, out any) error {
			if strings.Contains(query, "mergeable mergeStateStatus reviewDecision") {
				return json.Unmarshal([]byte(`{"repository":{"pullRequest":{"mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED","reviewDecision":"REVIEW_REQUIRED","commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE"}}}]}}}}`), out)
			}
			return original(query, vars, out)
		}
		plan, err := MergePreview("5")
		if err != nil {
			t.Fatal(err)
		}
		pr := plan.PRs[0]
		if pr.Mergeable != "MERGEABLE" || pr.MergeStateStatus != "BLOCKED" || pr.ReviewDecision != "REVIEW_REQUIRED" || pr.CIState != "FAILURE" {
			t.Fatalf("states %+v", pr)
		}
	})
	for _, tc := range []struct {
		target, numbers string
		stack           int
	}{{"3", "[1 2 3]", 10}, {"5", "[5]", 0}} {
		t.Run(tc.target, func(t *testing.T) {
			f, tty := mergeRepo(t, "")
			f.prs[2].draft = true
			plan, err := MergePreview(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			var numbers []int
			for _, pr := range plan.PRs {
				numbers = append(numbers, pr.Number)
			}
			if fmt.Sprint(numbers) != tc.numbers || plan.Stack != tc.stack || plan.Destination != "main" || plan.Repository != "o/r" {
				t.Fatalf("plan: %+v", plan)
			}
			if tc.target == "3" && (!plan.PRs[1].IsDraft || plan.PRs[1].HeadRefOid != "0000000000000000000000000000000000000002" || plan.PRs[1].Mergeable != "UNKNOWN" || plan.PRs[1].CIState != "UNKNOWN") {
				t.Errorf("member: %+v", plan.PRs[1])
			}
			if len(f.writes) != 0 || tty.shown.Len() != 0 {
				t.Fatalf("preview wrote %v or prompted %s", f.writes, tty.shown.String())
			}
		})
	}
}

func TestMergeChangedSelection(t *testing.T) {
	for _, change := range []string{"head", "membership", "base"} {
		t.Run(change, func(t *testing.T) {
			f, tty := mergeRepo(t, "y\n")
			original := graphQL
			graphQL = func(query string, vars map[string]any, out any) error {
				if strings.Contains(query, "search(query") && strings.Contains(tty.shown.String(), "Merge?") {
					switch change {
					case "head":
						f.prs[1].oid = "changed"
					case "membership":
						f.prs[1].state = "MERGED"
					case "base":
						f.prs[1].base = "release"
					}
				}
				return original(query, vars, out)
			}
			_, err := Merge("3", "")
			if !refusedWell(err) || len(f.writes) != 0 || !strings.Contains(tty.shown.String(), "Merge?") {
				t.Fatalf("changed %s: err %v, writes %v, terminal %s", change, err, f.writes, tty.shown.String())
			}
		})
	}
}

func TestReady(t *testing.T) {
	t.Run("no terminal", func(t *testing.T) {
		f, _ := mergeRepo(t, "")
		f.prs[2].draft = true
		openTTY = func() (io.ReadWriteCloser, error) { return nil, errors.New("no tty") }
		if _, err := Ready("3"); !refusedWell(err) || len(f.writes) != 0 {
			t.Fatalf("err %v, writes %v", err, f.writes)
		}
	})
	t.Run("partial failure resumes remaining drafts", func(t *testing.T) {
		f, _ := mergeRepo(t, "y\ny\n")
		f.prs[1].draft, f.prs[3].draft = true, true
		original := graphQL
		fail := true
		graphQL = func(query string, vars map[string]any, out any) error {
			if strings.Contains(query, "markPullRequestReadyForReview") && vars["id"] == "PR_3" && fail {
				fail = false
				return errors.New("service unavailable")
			}
			return original(query, vars, out)
		}
		got, err := Ready("3")
		if err == nil || fmt.Sprint(got.(ReadyResult).Ready) != "[1]" || f.prs[1].draft || !f.prs[3].draft {
			t.Fatalf("result %v, err %v", got, err)
		}
		useTTY(t, "y\n")
		got, err = Ready("3")
		if err != nil || fmt.Sprint(got.(ReadyResult).Ready) != "[3]" || f.prs[3].draft || fmt.Sprint(f.writes) != "[ready 1 ready 3]" {
			t.Fatalf("result %v, err %v, writes %v", got, err, f.writes)
		}
	})
	t.Run("changed selection", func(t *testing.T) {
		f, tty := mergeRepo(t, "y\n")
		f.prs[2].draft = true
		original := graphQL
		graphQL = func(query string, vars map[string]any, out any) error {
			if strings.Contains(query, "search(query") && strings.Contains(tty.shown.String(), "Mark ready?") {
				f.prs[1].oid = "changed"
			}
			return original(query, vars, out)
		}
		if _, err := Ready("3"); !refusedWell(err) || len(f.writes) != 0 || tty.shown.Len() == 0 {
			t.Fatalf("err %v, writes %v", err, f.writes)
		}
	})
	t.Run("drafts only and repeated run", func(t *testing.T) {
		f, tty := mergeRepo(t, "y\n")
		f.prs[1].draft, f.prs[3].draft = true, true
		got, err := Ready("3")
		if err != nil {
			t.Fatal(err)
		}
		if r := got.(ReadyResult); fmt.Sprint(r.Ready) != "[1 3]" || f.prs[1].draft || f.prs[3].draft || len(f.writes) != 2 {
			t.Fatalf("result %+v, writes %v", r, f.writes)
		}
		if !strings.Contains(tty.shown.String(), "#1 a") || !strings.Contains(tty.shown.String(), "#3 c") || strings.Contains(tty.shown.String(), "#4") {
			t.Fatalf("shown %s", tty.shown.String())
		}
		got, err = Ready("3")
		if err != nil {
			t.Fatal(err)
		}
		if r := got.(ReadyResult); len(r.Ready) != 0 || len(f.writes) != 2 {
			t.Fatalf("repeat %+v, writes %v", r, f.writes)
		}
	})
	t.Run("confirmation refused", func(t *testing.T) {
		f, tty := mergeRepo(t, "n\n")
		f.prs[2].draft = true
		got, err := Ready("3")
		if err != nil {
			t.Fatal(err)
		}
		if r := got.(ReadyResult); r.Skipped != "not confirmed" || len(f.writes) != 0 || tty.shown.Len() == 0 || !f.prs[2].draft {
			t.Fatalf("result %+v, writes %v", r, f.writes)
		}
	})
}
