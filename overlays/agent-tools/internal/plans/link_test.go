package plans

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// linkPlans makes two plans in c, the second titled title, and returns
// them with the second's file-level ID.
func linkPlans(t *testing.T, c Config, title string) (Plan, Plan, string) {
	t.Helper()
	p := newPlan(t, c, "x")
	made, err := c.New(NewOptions{Title: title, Slug: "other"}, day)
	if err != nil {
		t.Fatal(err)
	}
	other, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	id := fileID(fileText(t, other.file("")))
	if id == "" {
		t.Fatalf("%s has no file-level ID", other.file(""))
	}
	return p, other, id
}

// A plan links a related plan by its file-level ID, the node org-roam
// knows it by, under Related, with why it is related; the log records the
// link, and Related follows the log as References does.
func TestLink(t *testing.T) {
	t.Run("linked", func(t *testing.T) {
		c := testConfig(t)
		p, other, id := linkPlans(t, c, "The [other] plan")
		e, err := c.Link(p.file(""), other.file(""), "It splits work out of this one.", day)
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind != "linked" || e.Item != p.Name || e.Summary != "id:"+id {
			t.Errorf("event %+v", e)
		}
		want := "** References\n** Related\n- [[id:" + id + "][The (other) plan]]: It splits work out of this one.\n** Verification\n"
		if text := fileText(t, p.file("")); !strings.HasSuffix(text, want) {
			t.Errorf("plan:\n%s", text)
		}
		s, err := p.StatusAt(day)
		if err != nil || len(s.PropertyDrift) != 0 || len(s.StructureDrift) != 0 || len(s.DanglingLinks) != 0 {
			t.Errorf("status %v: %+v %+v %v", err, s.PropertyDrift, s.StructureDrift, s.DanglingLinks)
		}
	})

	// A second link to the same plan, whatever its reason, changes nothing.
	t.Run("again", func(t *testing.T) {
		c := testConfig(t)
		p, other, _ := linkPlans(t, c, "Other")
		if _, err := c.Link(p.file(""), other.file(""), "First.", day); err != nil {
			t.Fatal(err)
		}
		before := fileText(t, p.file(""))
		e, err := c.Link(p.file(""), other.Name, "Second.", day)
		if err != nil || e.Kind != "" {
			t.Errorf("again: %+v, %v", e, err)
		}
		if after := fileText(t, p.file("")); after != before {
			t.Errorf("plan changed:\n%s", after)
		}
		if events := eventsBut(t, p, "property"); len(events) != 1 {
			t.Errorf("events %+v", events)
		}
	})

	// A plan made before Related was a section gets one after References.
	t.Run("no Related yet", func(t *testing.T) {
		c := testConfig(t)
		p, other, id := linkPlans(t, c, "Other")
		os.WriteFile(p.file(""), []byte(strings.Replace(fileText(t, p.file("")), "** Related\n", "", 1)), 0o644)
		if _, err := c.Link(p.file(""), other.file(""), "Same area.", day); err != nil {
			t.Fatal(err)
		}
		if text := fileText(t, p.file("")); !strings.HasSuffix(text, "** References\n** Related\n- [[id:"+id+"][Other]]: Same area.\n** Verification\n") {
			t.Errorf("plan:\n%s", text)
		}
	})

	// A Related line the log does not give, or a linked event with no
	// line, is property drift.
	t.Run("drift", func(t *testing.T) {
		c := testConfig(t)
		p, other, _ := linkPlans(t, c, "Other")
		if _, err := c.Link(p.file(""), other.file(""), "Same area.", day); err != nil {
			t.Fatal(err)
		}
		edited := regexp.MustCompile(`id:[0-9a-f-]+\]`).ReplaceAllString(fileText(t, p.file("")), "id:"+fileID(fileText(t, p.file("")))+"]")
		os.WriteFile(p.file(""), []byte(edited), 0o644)
		s, err := p.StatusAt(day)
		if err != nil || len(s.PropertyDrift) != 1 || s.PropertyDrift[0].Property != "Related" {
			t.Errorf("drift %v: %+v", err, s.PropertyDrift)
		}
	})

	for what, args := range map[string]func(Plan, Plan) (string, string, string){
		"the plan itself":        func(p, _ Plan) (string, string, string) { return p.file(""), p.Name, "x" },
		"a part's file":          func(p, o Plan) (string, string, string) { return p.file(""), o.file("P1"), "x" },
		"a part's name":          func(p, o Plan) (string, string, string) { return p.file(""), o.Name + ".P1", "x" },
		"a why on two lines":     func(p, o Plan) (string, string, string) { return p.file(""), o.file(""), "x\n* forged" },
		"no why":                 func(p, o Plan) (string, string, string) { return p.file(""), o.file(""), " " },
		"a plan in the old form": func(p, o Plan) (string, string, string) { return p.file(""), "old", "x" },
		"not a plan":             func(p, o Plan) (string, string, string) { return p.file(""), "nowhere", "x" },
		"an id link in why": func(p, o Plan) (string, string, string) {
			return p.file(""), o.file(""), "See [[id:11111111-2222-4333-8444-555555555555][another plan]]"
		},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			c := testConfig(t)
			p, other, _ := linkPlans(t, c, "Other")
			addItems(t, other.file(""), "** Distribution", "P1")
			if _, err := c.New(NewOptions{Title: "Part", Parent: other.file(""), Part: "P1"}, day); err != nil {
				t.Fatal(err)
			}
			old := filepath.Join(c.Dir, "2026-10-14-old.box")
			os.WriteFile(old+".org", []byte("#+title: Old\n* Plan\n:PROPERTIES:\n:ID: 2026-10-14-old.box\n:END:\n"), 0o644)
			os.WriteFile(old+".log.org", nil, 0o644)
			plan, target, why := args(p, other)
			if target == "old" {
				target = old + ".org"
			}
			before := fileText(t, p.file(""))
			_, err := c.Link(plan, target, why, day)
			isRefusal(t, what, err)
			if after := fileText(t, p.file("")); after != before {
				t.Errorf("%s: the plan changed:\n%s", what, after)
			}
		})
	}

	t.Run("refused/another host's plan", func(t *testing.T) {
		c := testConfig(t)
		p, other, _ := linkPlans(t, c, "Other")
		theirs := otherHosts(t, p)
		_, err := c.Link(theirs.file(""), other.file(""), "x", day)
		isRefusal(t, "another host's plan", err)
	})
}

// A Related link resolves to a file-level ID of any Org file in the plans'
// repository, a plan's or a note's; status lists one that resolves to none.
func TestDanglingLinks(t *testing.T) {
	c := testConfig(t)
	p, other, id := linkPlans(t, c, "Other")
	if _, err := c.Link(p.file(""), other.file(""), "Same area.", day); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(filepath.Dir(c.Dir), "20261023150000-next_session.org")
	os.WriteFile(note, []byte(":PROPERTIES:\n:ID: 11111111-2222-4333-8444-555555555555\n:END:\n#+title: Next session\n"), 0o644)
	insertUnder(t, p.file(""), "** Related", "- [[id:11111111-2222-4333-8444-555555555555][Next session]]: A note.\n"+
		"- [[id:99999999-2222-4333-8444-555555555555][Gone]]: Deleted.\n")
	s, err := p.StatusAt(day)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.DanglingLinks, " ") != "id:99999999-2222-4333-8444-555555555555" {
		t.Errorf("dangling %v (the linked plan is %s)", s.DanglingLinks, id)
	}
	if !strings.Contains(s.Org(), "- Related links to nothing: id:99999999-2222-4333-8444-555555555555\n") {
		t.Errorf("status:\n%s", s.Org())
	}
}

// Related holds a list, not headings.
func TestRelatedStructure(t *testing.T) {
	got := structureOf(planOrg("** Related\n- [[id:x][X]]: why\n*** A heading\n"))
	if len(got) != 1 || got[0].problem != "a heading under Related, which holds text" {
		t.Errorf("drift %+v", got)
	}
}
