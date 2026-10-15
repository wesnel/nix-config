package plans

import (
	"fmt"
	"strings"
	"testing"
)

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
		for name, values := range map[string][]string{"STATUS": {"working"}, "OWNERS": {"/w=a"},
			"BRANCHES": {"two words"}} {
			isRefusal(t, name, p.SetProperty("", name, values, day))
		}
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
