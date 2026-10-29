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
