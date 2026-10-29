package plans

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A workstream is an org-roam node file beside the plans, holding only a
// title and the summary a person writes.
func TestNewWorkstream(t *testing.T) {
	t.Run("written", func(t *testing.T) {
		c := testConfig(t)
		w, err := c.NewWorkstream("Agent tooling", "The agent-tools overlay and the skills.\nNot Codex parity.")
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(c.Dir, "workstream-agent-tooling.box.org")
		if w.File != file || w.Title != "Agent tooling" || w.Existed {
			t.Errorf("workstream %+v", w)
		}
		text := fileText(t, file)
		want := ":PROPERTIES:\n:ID: " + w.ID + "\n:END:\n#+title: Agent tooling\n\nThe agent-tools overlay and the skills.\nNot Codex parity.\n"
		if text != want || len(w.ID) != 36 {
			t.Errorf("file:\n%s", text)
		}
	})

	// A second run, whatever its summary, gives the same workstream and
	// leaves its file as the person last wrote it.
	t.Run("again", func(t *testing.T) {
		c := testConfig(t)
		first, err := c.NewWorkstream("Agent tooling", "")
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(first.File, []byte(fileText(t, first.File)+"Edited by hand.\n"), 0o644)
		before := fileText(t, first.File)
		again, err := c.NewWorkstream("Agent tooling!", "Another summary.")
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != first.ID || !again.Existed || fileText(t, first.File) != before {
			t.Errorf("again %+v; file:\n%s", again, fileText(t, first.File))
		}
	})

	// A workstream another host made is the same line of work: this host
	// makes no copy of it.
	t.Run("another host's", func(t *testing.T) {
		c := testConfig(t)
		mac := Config{Dir: c.Dir, Host: "mac"}
		theirs, err := mac.NewWorkstream("Codex parity", "")
		if err != nil {
			t.Fatal(err)
		}
		mine, err := c.NewWorkstream("Codex parity", "")
		if err != nil || mine.ID != theirs.ID || mine.Host != "mac" || !mine.Existed {
			t.Errorf("box's %+v, %v", mine, err)
		}
		if _, err := os.Stat(filepath.Join(c.Dir, "workstream-codex-parity.box.org")); err == nil {
			t.Error("box wrote a copy of mac's workstream")
		}
	})

	t.Run("refused", func(t *testing.T) {
		c := testConfig(t)
		for name, args := range map[string][2]string{
			"no title":           {"", ""},
			"no slug":            {"!!", ""},
			"title on two lines": {"Agent\ntooling", ""},
			"summary heading":    {"Agent tooling", "* Not a summary"},
			"summary drawer":     {"Agent tooling", ":PROPERTIES:"},
		} {
			_, err := c.NewWorkstream(args[0], args[1])
			isRefusal(t, name, err)
		}
		if entries, _ := os.ReadDir(c.Dir); len(entries) > 1 {
			t.Errorf("refused workstreams left files: %v", entries)
		}
	})
}

// planOn makes host's plan titled title on branches, in workstream ws, or
// none for "".
func planOn(t *testing.T, c Config, slug, ws string, branches ...string) Plan {
	t.Helper()
	made, err := c.New(NewOptions{Title: "Plan " + slug, Slug: slug, Repos: []string{"o/r"}, Branches: branches,
		Workstream: ws}, day)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// workstreamOf is the ID of p's workstream, or "".
func workstreamOf(t *testing.T, p Plan) string {
	t.Helper()
	ref, err := p.Workstream()
	if err != nil {
		t.Fatal(err)
	}
	return ref.ID
}

// A plan names its workstream by an id: link in WORKSTREAM, which new
// writes from --workstream or else takes from the plan whose branch the new
// plan continues; a part is in its plan's.
func TestPlanWorkstream(t *testing.T) {
	t.Run("named", func(t *testing.T) {
		c := testConfig(t)
		w, err := c.NewWorkstream("Agent [tooling]", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"agent-tooling", "workstream-agent-tooling.box", w.File} {
			p := planOn(t, c, "x", name, "main..loop")
			if !strings.Contains(fileText(t, p.file("")), "\n:WORKSTREAM: [[id:"+w.ID+"][Agent (tooling)]]\n") {
				t.Errorf("%s: plan:\n%s", name, fileText(t, p.file("")))
			}
			ref, err := p.Workstream()
			if err != nil || ref.ID != w.ID || ref.Title != "Agent (tooling)" {
				t.Errorf("%s: workstream %+v, %v", name, ref, err)
			}
			s, err := p.StatusAt(day)
			if err != nil || len(s.PropertyDrift) != 0 || len(s.StructureDrift) != 0 || len(s.DanglingLinks) != 0 {
				t.Errorf("%s: status %v: %+v %+v %v", name, err, s.PropertyDrift, s.StructureDrift, s.DanglingLinks)
			}
		}
	})

	t.Run("unknown", func(t *testing.T) {
		c := testConfig(t)
		_, err := c.New(NewOptions{Title: "X", Slug: "x", Workstream: "agent-tooling"}, day)
		isRefusal(t, "unknown workstream", err)
		if ps, _ := c.ownPlans(); len(ps) != 0 {
			t.Errorf("a plan was made: %v", ps)
		}
	})

	// Without --workstream, a plan that works on another plan's branch, or
	// stacks on it, is in that plan's workstream, whichever host's it is;
	// a default branch such as main says nothing.
	t.Run("continues a branch", func(t *testing.T) {
		c := testConfig(t)
		mac := Config{Dir: c.Dir, Host: "mac"}
		w, err := mac.NewWorkstream("Agent tooling", "")
		if err != nil {
			t.Fatal(err)
		}
		planOn(t, mac, "loop", "agent-tooling", "main..loop")
		planOn(t, c, "unrelated", "", "main..other")
		for slug, branches := range map[string][]string{
			"stacked":     {"loop..loop-flag"},
			"same branch": {"main..loop"},
			"bare branch": {"loop"},
		} {
			if got := workstreamOf(t, planOn(t, c, strings.ReplaceAll(slug, " ", "-"), "", branches...)); got != w.ID {
				t.Errorf("%s: workstream %q, want %q", slug, got, w.ID)
			}
		}
		for slug, branches := range map[string][]string{
			"on main":   {"main..fresh"},
			"no branch": nil,
		} {
			if got := workstreamOf(t, planOn(t, c, strings.ReplaceAll(slug, " ", "-"), "", branches...)); got != "" {
				t.Errorf("%s: workstream %q, want none", slug, got)
			}
		}
	})

	// A part is in its plan's workstream: its file names none, and a part
	// cannot name another.
	t.Run("part", func(t *testing.T) {
		c := testConfig(t)
		if _, err := c.NewWorkstream("Agent tooling", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := c.NewWorkstream("Codex parity", ""); err != nil {
			t.Fatal(err)
		}
		p := planOn(t, c, "x", "agent-tooling", "main..loop")
		addItems(t, p.file(""), "** Distribution", "P1", "P2")
		_, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1", Workstream: "codex-parity"}, day)
		isRefusal(t, "another workstream", err)
		if _, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P2", Branches: []string{"loop..flag"}}, day); err != nil {
			t.Fatal(err)
		}
		if text := fileText(t, p.file("P2")); strings.Contains(text, "WORKSTREAM") {
			t.Errorf("part:\n%s", text)
		}
	})
}

// Workstreams lists every host's workstreams, and nothing else in the
// plans directory.
func TestWorkstreams(t *testing.T) {
	c := testConfig(t)
	newPlan(t, c, "x")
	mac := Config{Dir: c.Dir, Host: "mac"}
	if _, err := mac.NewWorkstream("Codex parity", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NewWorkstream("Agent tooling", ""); err != nil {
		t.Fatal(err)
	}
	ws, err := c.Workstreams()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, w := range ws {
		got = append(got, w.Title+"@"+w.Host+"="+w.Name)
	}
	if strings.Join(got, " ") != "Agent tooling@box=workstream-agent-tooling.box Codex parity@mac=workstream-codex-parity.mac" {
		t.Errorf("workstreams %v", got)
	}
}
