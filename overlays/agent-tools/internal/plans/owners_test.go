package plans

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwners(t *testing.T) {
	t.Run("none before any owner", func(t *testing.T) {
		c := testConfig(t)
		newPlan(t, c, "x")
		if owners, err := c.Owners(); err != nil || len(owners) != 0 {
			t.Fatalf("before any owner: %v %v", owners, err)
		}
	})

	t.Run("set", func(t *testing.T) {
		c := testConfig(t)
		wt, _ := filepath.EvalSymlinks(t.TempDir())
		other, _ := filepath.EvalSymlinks(t.TempDir())
		p := newPlan(t, c, "x")
		if _, err := p.SetOwner(wt, "a1", day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.SetOwner(other, "main", day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.SetOwner(wt, "a2", day); err != nil {
			t.Fatal(err)
		}
		owners, err := c.Owners()
		if err != nil || owners[wt] != "a2" || owners[other] != "main" || len(owners) != 2 {
			t.Errorf("owners: %v %v", owners, err)
		}
		data, _ := os.ReadFile(p.File())
		if strings.Count(string(data), ":OWNERS:") != 1 || !strings.Contains(string(data), ":OWNERS: "+wt+"=a2 "+other+"=main\n") {
			t.Errorf("plan file:\n%s", data)
		}
		if found, _ := c.Find(FindOptions{Session: "abc"}); len(found) != 1 {
			t.Errorf("the plan no longer parses: %v", found)
		}
	})

	// Each case is a worktree and an owner; WT stands for a worktree of the
	// test's own.
	for name, bad := range map[string][2]string{
		"relative worktree":    {"relative/wt", "a1"},
		"owner of two words":   {"WT", "two words"},
		"owner with an equals": {"WT", "a=b"},
		"no owner":             {"WT", ""},
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			wt, _ := filepath.EvalSymlinks(t.TempDir())
			p := newPlan(t, testConfig(t), "x")
			dir := strings.Replace(bad[0], "WT", wt, 1)
			_, err := p.SetOwner(dir, bad[1], day)
			isRefusal(t, "Own "+dir+" "+bad[1], err)
		})
	}

	t.Run("refused/another host's plan", func(t *testing.T) {
		wt, _ := filepath.EvalSymlinks(t.TempDir())
		p := newPlan(t, testConfig(t), "x")
		_, err := otherHosts(t, p).SetOwner(wt, "a3", day)
		isRefusal(t, "another host's plan", err)
	})
}
