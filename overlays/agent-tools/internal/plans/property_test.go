package plans

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// setProp sets a property of a plan file's top heading.
func setProp(t *testing.T, file, name, value string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, ":"+name+":") {
			line = ":" + name + ": " + value
		}
		lines = append(lines, line)
	}
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// propertyEvents are the property events in p's log, as "ITEM NAME = value".
func propertyEvents(t *testing.T, p Plan) []string {
	t.Helper()
	events, err := p.Events()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		if e.Kind == "property" {
			out = append(out, fmt.Sprintf("%s %s = %s", e.Item, e.Property, e.Value))
		}
	}
	return out
}

func drift(t *testing.T, p Plan) []string {
	t.Helper()
	s, err := p.Status()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range s.PropertyDrift {
		out = append(out, fmt.Sprintf("%s %s: file %q, log %q", d.ID, d.Property, d.File, d.Log))
	}
	return out
}

func expectList(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\n%s\nwant:\n%s", what, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A plan's WORKTREES, BRANCHES, PULL_REQUESTS and OWNERS, and its
// references, are a projection of its log: every change through agent-plan
// is logged with the whole new value, and status reports a value its log
// does not give it.
func TestProperties(t *testing.T) {
	t.Run("logged from the start", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		expectList(t, "logged", propertyEvents(t, p), p.Name+" WORKTREES = /w/loop", p.Name+" BRANCHES = main..loop")
		expectList(t, "drift", drift(t, p))
	})

	t.Run("each change", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		if _, err := p.AddToProperty("", "BRANCHES", "loop..flag", day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.AddToProperty("", "BRANCHES", "loop..flag", day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.SetOwner("/w/loop", "a1", day); err != nil {
			t.Fatal(err)
		}
		if err := p.SetProperty("", "WORKTREES", []string{"/w/flag"}, day); err != nil {
			t.Fatal(err)
		}
		expectList(t, "logged", propertyEvents(t, p), p.Name+" WORKTREES = /w/loop", p.Name+" BRANCHES = main..loop",
			p.Name+" BRANCHES = main..loop loop..flag", p.Name+" OWNERS = /w/loop=a1", p.Name+" WORKTREES = /w/flag")
		expectList(t, "drift", drift(t, p))
		if got := planProps(t, p.File())["WORKTREES"]; got != "/w/flag" {
			t.Errorf("WORKTREES %q", got)
		}
	})

	t.Run("a hand edit", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		setProp(t, p.File(), "BRANCHES", "main..loop main..other")
		setProp(t, p.File(), "PULL_REQUESTS", "https://github.com/o/r/pull/1")
		insertUnder(t, p.File(), "** References", "*** https://example.com/x\n:PROPERTIES:\n:URL: https://example.com/x\n:END:\n")
		expectList(t, "drift", drift(t, p),
			p.Name+` BRANCHES: file "main..loop main..other", log "main..loop"`,
			p.Name+` PULL_REQUESTS: file "https://github.com/o/r/pull/1", log ""`,
			p.Name+` References: file "https://example.com/x", log ""`)
		s, _ := p.Status()
		if !strings.Contains(s.Org(), "- Property drift: "+p.Name+" BRANCHES (file main..loop main..other, log main..loop)") {
			t.Errorf("status:\n%s", s.Org())
		}
		if err := p.SetProperty("", "BRANCHES", []string{"main..loop", "main..other"}, day); err != nil {
			t.Fatal(err)
		}
		if err := p.SetProperty("", "PULL_REQUESTS", []string{"https://github.com/o/r/pull/1"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Reference("https://example.com/x", "Used for x.", day); err != nil {
			t.Fatal(err)
		}
		expectList(t, "drift after logging", drift(t, p))
	})

	// A plan whose log has never recorded a property is not compared.
	t.Run("never logged", func(t *testing.T) {
		c := testConfig(t)
		made, err := c.New(NewOptions{Title: "Old", Slug: "old"}, day)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := c.Open(made.Plan)
		setProp(t, p.File(), "BRANCHES", "main..loop")
		expectList(t, "drift", drift(t, p))
	})

	t.Run("refused", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		for name, values := range map[string][]string{"PARENT": {"x"}, "OWNERS": {"/w=a"},
			"BRANCHES": {"two words"}} {
			isRefusal(t, name, p.SetProperty("", name, values, day))
		}
	})
}

// RemoveFromProperty takes one value a hook or an agent filed into the wrong
// plan back out of its WORKTREES, BRANCHES or PULL_REQUESTS, and logs the
// new value with why, so status still finds the file and the log agree.
func TestRemoveFromProperty(t *testing.T) {
	// summaries are the summaries of p's property events about item.
	summaries := func(t *testing.T, p Plan, item string) []string {
		t.Helper()
		events, err := p.Events()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range events {
			if e.Kind == "property" && e.Item == item {
				out = append(out, e.Summary)
			}
		}
		return out
	}

	t.Run("removed and logged with the reason", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		if _, err := p.AddToProperty("", "BRANCHES", "main..other", day); err != nil {
			t.Fatal(err)
		}
		if err := p.RemoveFromProperty("", "BRANCHES", "main..other", "It is plan y's work.", day); err != nil {
			t.Fatal(err)
		}
		if got := planProps(t, p.File())["BRANCHES"]; got != "main..loop" {
			t.Errorf("BRANCHES %q", got)
		}
		expectList(t, "logged", propertyEvents(t, p), p.Name+" WORKTREES = /w/loop", p.Name+" BRANCHES = main..loop",
			p.Name+" BRANCHES = main..loop main..other", p.Name+" BRANCHES = main..loop")
		got := summaries(t, p, p.Name)
		if last := got[len(got)-1]; last != "BRANCHES main..loop; removed main..other: It is plan y's work." {
			t.Errorf("summary %q", last)
		}
		expectList(t, "drift", drift(t, p))
	})

	t.Run("the last value", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		if err := p.RemoveFromProperty("", "WORKTREES", "/w/loop", "Another session's worktree.", day); err != nil {
			t.Fatal(err)
		}
		if got := planProps(t, p.File())["WORKTREES"]; got != "" {
			t.Errorf("WORKTREES %q", got)
		}
		expectList(t, "drift", drift(t, p))
	})

	// A value only the file holds, as a hand edit leaves, is removed and
	// logged too, so its reason is on record.
	t.Run("a value the log never gave", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		setProp(t, p.File(), "PULL_REQUESTS", "https://github.com/o/r/pull/1")
		if err := p.RemoveFromProperty("", "PULL_REQUESTS", "https://github.com/o/r/pull/1", "Opened by another session.", day); err != nil {
			t.Fatal(err)
		}
		got := summaries(t, p, p.Name)
		if last := got[len(got)-1]; last != "PULL_REQUESTS; removed https://github.com/o/r/pull/1: Opened by another session." {
			t.Errorf("summary %q", last)
		}
		expectList(t, "drift", drift(t, p))
	})

	t.Run("a part's", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Distribution", "P1")
		if _, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1", Branches: []string{"loop..flag", "main..other"}}, day); err != nil {
			t.Fatal(err)
		}
		if err := p.RemoveFromProperty("P1", "BRANCHES", "main..other", "It is plan y's work.", day); err != nil {
			t.Fatal(err)
		}
		if got := planProps(t, p.file("P1"))["BRANCHES"]; got != "loop..flag" {
			t.Errorf("part's BRANCHES %q", got)
		}
		if got := planProps(t, p.File())["BRANCHES"]; got != "main..loop" {
			t.Errorf("plan's BRANCHES %q", got)
		}
		expectList(t, "drift", drift(t, p))
	})

	t.Run("refused", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		before, err := os.ReadFile(p.File())
		if err != nil {
			t.Fatal(err)
		}
		for what, args := range map[string][3]string{
			"a value the plan does not hold": {"BRANCHES", "main..other", "x"},
			"OWNERS, own's":                  {"OWNERS", "/w/loop=a1", "x"},
			"a property not logged":          {"STATUS", "planning", "x"},
			"no reason":                      {"BRANCHES", "main..loop", ""},
			"two values":                     {"BRANCHES", "main..loop main..x", "x"},
		} {
			isRefusal(t, what, p.RemoveFromProperty("", args[0], args[1], args[2], day))
		}
		isRefusal(t, "another host's plan", otherHosts(t, p).RemoveFromProperty("", "BRANCHES", "main..loop", "x", day))
		if after, _ := os.ReadFile(p.File()); string(after) != string(before) {
			t.Errorf("refused removals changed the plan:\n%s", after)
		}
		expectList(t, "logged", propertyEvents(t, p), p.Name+" WORKTREES = /w/loop", p.Name+" BRANCHES = main..loop")
	})
}

// planProps are the properties of file's `* Plan` heading.
func planProps(t *testing.T, file string) map[string]string {
	t.Helper()
	props, err := planHeading(file)
	if err != nil {
		t.Fatal(err)
	}
	return props
}
