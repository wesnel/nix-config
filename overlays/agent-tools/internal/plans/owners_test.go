package plans

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwners(t *testing.T) {
	c := testConfig(t)
	wt, _ := filepath.EvalSymlinks(t.TempDir())
	other, _ := filepath.EvalSymlinks(t.TempDir())
	p := newPlan(t, c, "x")

	if owners, err := c.Owners(); err != nil || len(owners) != 0 {
		t.Fatalf("before any owner: %v %v", owners, err)
	}
	if _, err := p.SetOwner(wt, "a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.SetOwner(other, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.SetOwner(wt, "a2"); err != nil {
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
	for _, bad := range [][2]string{{"relative/wt", "a1"}, {wt, "two words"}, {wt, "a=b"}, {wt, ""}} {
		_, err := p.SetOwner(bad[0], bad[1])
		isRefusal(t, "Own "+bad[0]+" "+bad[1], err)
	}
	theirs := newPlan(t, Config{Dir: c.Dir, Host: "other"}, "y")
	other2, _ := Config{Dir: c.Dir, Host: "box"}.Open(theirs.file(""))
	_, err = other2.SetOwner(wt, "a3")
	isRefusal(t, "another host's plan", err)
}
