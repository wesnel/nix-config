package loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gh"
)

// mergeAsync answers PUT pulls/N/merge-async as GitHub's REST docs ("Merge
// a pull request asynchronously") describe it: 400 for a pull request not
// ready to merge, such as a draft or one not open; otherwise it merges N
// and every open pull request below it in its stack, and answers 202 with
// status pending and the request's uuid. GitHub is faked since merging
// there cannot be undone.
func (f *fakeStacks) mergeAsync(method, path string, body, out any) error {
	f.writes = append(f.writes, method+" "+path)
	f.bodies = append(f.bodies, body.(map[string]any))
	if err := f.fail; err != nil {
		f.fail = nil
		return err
	}
	var number int
	if _, err := fmt.Sscanf(path, "repos/o/r/pulls/%d/merge-async", &number); err != nil || method != "PUT" {
		return fmt.Errorf("unexpected %s %s", method, path)
	}
	merged := []int{number}
	if s := f.stackOf(number); s != 0 {
		merged = nil
		for _, m := range f.stacks[s] {
			if f.prs[m].state == "OPEN" {
				merged = append(merged, m)
			}
			if m == number {
				break
			}
		}
	}
	for _, m := range merged {
		if pr := f.prs[m]; pr.draft || pr.state != "OPEN" {
			return &gh.StatusError{Code: 400}
		}
	}
	for _, m := range merged {
		f.prs[m].state = "MERGED"
	}
	encoded, _ := json.Marshal(map[string]any{"status": "pending",
		"details": map[string]any{"uuid": fmt.Sprintf("u-%d", number), "message": "Merge request accepted"}})
	return json.Unmarshal(encoded, out)
}

// mergeRepo is stacksRepo with merge-async faked, PRs 1 to 4 ready for
// review and stacked as GitHub stack 10, and the operator's terminal
// answering answers.
func mergeRepo(t *testing.T, answers string) (*fakeStacks, *fakeTTY) {
	t.Helper()
	f := stacksRepo(t)
	for _, n := range []int{1, 2, 3, 4} {
		f.prs[n].draft = false
	}
	f.stacks[10] = []int{1, 2, 3, 4}
	old := mergeREST
	mergeREST = f.mergeAsync
	t.Cleanup(func() { mergeREST = old })
	return f, useTTY(t, answers)
}

func TestMerge(t *testing.T) {
	t.Run("the top and every open PR below it", func(t *testing.T) {
		f, tty := mergeRepo(t, "y\n")
		got, err := Merge("3", "")
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(f.writes) != "[PUT repos/o/r/pulls/3/merge-async]" {
			t.Fatalf("writes: %v", f.writes)
		}
		// The head the operator was shown is the head merged.
		if b := f.bodies[0]; b["sha"] != "0000000000000000000000000000000000000003" || b["merge_method"] != "merge" {
			t.Errorf("body: %v", b)
		}
		r := got.(MergeResult)
		if fmt.Sprint(r.PRs) != "[1 2 3]" || r.Status != "pending" || r.UUID != "u-3" || r.Stack != 10 {
			t.Errorf("result: %+v", r)
		}
		shown := tty.shown.String()
		for _, want := range []string{"#1 a", "#2 b", "#3 c", "title c", "into main"} {
			if !strings.Contains(shown, want) {
				t.Errorf("the terminal was not shown %q:\n%s", want, shown)
			}
		}
		if strings.Contains(shown, "#4") {
			t.Errorf("the terminal was shown #4, above the top:\n%s", shown)
		}
		if f.prs[4].state != "OPEN" || f.prs[1].state != "MERGED" {
			t.Errorf("states: #1 %s, #4 %s", f.prs[1].state, f.prs[4].state)
		}
	})

	t.Run("a merged PR below is left out", func(t *testing.T) {
		f, tty := mergeRepo(t, "y\n")
		f.prs[1].state = "MERGED"
		got, err := Merge("c", "squash")
		if err != nil {
			t.Fatal(err)
		}
		if r := got.(MergeResult); fmt.Sprint(r.PRs) != "[2 3]" || len(f.bodies) != 1 || f.bodies[0]["merge_method"] != "squash" {
			t.Errorf("result %+v, bodies %v", r, f.bodies)
		}
		if strings.Contains(tty.shown.String(), "#1 ") {
			t.Errorf("the terminal was shown the merged #1:\n%s", tty.shown.String())
		}
	})

	t.Run("a PR in no stack", func(t *testing.T) {
		f, _ := mergeRepo(t, "y\n")
		got, err := Merge("5", "")
		if err != nil {
			t.Fatal(err)
		}
		if r := got.(MergeResult); fmt.Sprint(r.PRs) != "[5]" || r.Stack != 0 || len(f.writes) != 1 {
			t.Errorf("result %+v, writes %v", r, f.writes)
		}
	})

	t.Run("not confirmed", func(t *testing.T) {
		f, _ := mergeRepo(t, "n\n")
		got, err := Merge("3", "")
		if err != nil {
			t.Fatal(err)
		}
		if r := got.(MergeResult); r.Skipped == "" || r.Status != "" || len(f.writes) != 0 {
			t.Errorf("result %+v, writes %v", r, f.writes)
		}
	})

	for name, setup := range map[string]func(f *fakeStacks) (target, method string){
		"a draft below":           func(f *fakeStacks) (string, string) { f.prs[2].draft = true; return "3", "" },
		"a draft top":             func(f *fakeStacks) (string, string) { f.prs[3].draft = true; return "3", "" },
		"a draft in no stack":     func(f *fakeStacks) (string, string) { f.prs[5].draft = true; return "5", "" },
		"someone else's top":      func(*fakeStacks) (string, string) { return "6", "" },
		"a default branch":        func(*fakeStacks) (string, string) { return "main", "" },
		"an unknown merge method": func(*fakeStacks) (string, string) { return "3", "fast-forward" },
		"someone else's PR below": func(f *fakeStacks) (string, string) {
			f.prs[9] = &fakeStackPR{head: "y", base: "x", author: "me", state: "OPEN"}
			f.stacks[11] = []int{6, 9}
			return "9", ""
		},
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			f, tty := mergeRepo(t, "y\n")
			target, method := setup(f)
			if _, err := Merge(target, method); !refusedWell(err) {
				t.Errorf("%s: %v", name, err)
			}
			if len(f.writes) != 0 || tty.shown.Len() != 0 {
				t.Errorf("%s: wrote %v, showed %q", name, f.writes, tty.shown.String())
			}
		})
	}

	t.Run("refused/no terminal", func(t *testing.T) {
		f, _ := mergeRepo(t, "")
		openTTY = func() (io.ReadWriteCloser, error) { return nil, errors.New("device not configured") }
		if _, err := Merge("3", ""); !refusedWell(err) || len(f.writes) != 0 {
			t.Errorf("no terminal: %v, writes %v", err, f.writes)
		}
	})

	// GitHub's own refusal is reported as a failure, not a policy refusal.
	t.Run("GitHub's 409", func(t *testing.T) {
		f, _ := mergeRepo(t, "y\n")
		f.fail = &gh.StatusError{Code: 409}
		if _, err := Merge("3", ""); err == nil || refused(err) || len(f.writes) != 1 {
			t.Errorf("a 409: %v, writes %v", err, f.writes)
		}
	})
}
