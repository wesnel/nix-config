package plans

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every change to a plan's files is made under the log's lock, so
// concurrent agent-plan calls neither lose a write nor reuse an ID.
func TestLock(t *testing.T) {
	t.Run("concurrent asks", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		var wg sync.WaitGroup
		for i := range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := p.Ask(AskOptions{Question: fmt.Sprintf("Question %d?", i), Recommendation: "yes"}, day); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		data, _ := os.ReadFile(p.file(""))
		inFile := regexp.MustCompile(`(?m)^:ID: (Q[0-9]+)$`).FindAllStringSubmatch(string(data), -1)
		fileIDs := map[string]bool{}
		for _, m := range inFile {
			fileIDs[m[1]] = true
		}
		if len(inFile) != 20 || len(fileIDs) != 20 {
			t.Errorf("the file holds %d questions, %d distinct:\n%s", len(inFile), len(fileIDs), data)
		}
		events := eventsBut(t, p, "property")
		logIDs := map[string]bool{}
		for _, e := range events {
			if e.Kind == "asked" {
				logIDs[e.Item] = true
			}
		}
		if len(events) != 20 || len(logIDs) != 20 {
			t.Errorf("the log holds %d events, %d distinct items", len(events), len(logIDs))
		}
		for id := range fileIDs {
			if !logIDs[id] {
				t.Errorf("%s is in the file and not the log", id)
			}
		}
	})

	t.Run("a property added while asking", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		var wg sync.WaitGroup
		for i := range 10 {
			wg.Add(2)
			go func() {
				defer wg.Done()
				if _, err := p.Ask(AskOptions{Question: fmt.Sprintf("Question %d?", i), Recommendation: "yes"}, day); err != nil {
					t.Error(err)
				}
			}()
			go func() {
				defer wg.Done()
				if _, err := p.AddToProperty("", "BRANCHES", fmt.Sprintf("b%d", i), day); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		data, _ := os.ReadFile(p.file(""))
		text := string(data)
		if n := strings.Count(text, ":RECOMMENDATION: yes\n"); n != 10 {
			t.Errorf("%d of 10 questions are in the file:\n%s", n, text)
		}
		branches := regexp.MustCompile(`(?m)^:BRANCHES: (.*)$`).FindStringSubmatch(text)
		for i := range 10 {
			if branches == nil || !slices.Contains(strings.Fields(branches[1]), fmt.Sprintf("b%d", i)) {
				t.Errorf("b%d is not in BRANCHES: %v", i, branches)
			}
		}
	})

	// Emacs holds a lock symlink beside a file whose buffer has unsaved
	// changes; a rename over it now would be lost or overwritten at its next
	// save.
	t.Run("refused/a file Emacs is editing", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		lockLink := filepath.Join(p.Dir, ".#"+filepath.Base(p.file("")))
		if err := os.Symlink("wgn@box.1234:1700000000", lockLink); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(p.file(""))
		_, err := p.AddToProperty("", "BRANCHES", "other", day)
		isRefusal(t, "a file Emacs is editing", err)
		if after, _ := os.ReadFile(p.file("")); string(after) != string(before) {
			t.Errorf("the file changed:\n%s", after)
		}
	})

	// A file changed between edit's read and its rename, as an Emacs save
	// would, is read again and the edit applied to what is there.
	t.Run("a file changed during an edit", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		file := p.file("")
		calls := 0
		err := p.withLock(func(l *lockedPlan) error {
			return l.edit(file, func(lines []string) ([]string, error) {
				calls++
				if calls == 1 {
					data, _ := os.ReadFile(file)
					os.WriteFile(file, []byte(string(data)+"Saved from Emacs.\n"), 0o644)
				}
				return append(lines[:len(lines)-1], "Added.", ""), nil
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(file)
		if calls != 2 || !strings.HasSuffix(string(data), "Saved from Emacs.\nAdded.\n") {
			t.Errorf("after %d calls:\n%s", calls, data)
		}
	})

	t.Run("refused/a file that keeps changing", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		file := p.file("")
		err := p.withLock(func(l *lockedPlan) error {
			return l.edit(file, func(lines []string) ([]string, error) {
				data, _ := os.ReadFile(file)
				os.WriteFile(file, []byte(string(data)+"x\n"), 0o644)
				return append(lines, "Added."), nil
			})
		})
		isRefusal(t, "a file that keeps changing", err)
		if err != nil && !strings.Contains(err.Error(), "kept changing") {
			t.Errorf("refused for another reason: %v", err)
		}
	})

	t.Run("an edit that changes nothing", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		file := p.file("")
		old := time.Now().Add(-time.Hour).Truncate(time.Second)
		os.Chtimes(file, old, old)
		err := p.withLock(func(l *lockedPlan) error {
			return l.edit(file, func(lines []string) ([]string, error) { return lines, nil })
		})
		if err != nil {
			t.Fatal(err)
		}
		if info, _ := os.Stat(file); !info.ModTime().Equal(old) {
			t.Errorf("the file was written: modified %v, want %v", info.ModTime(), old)
		}
	})
}
