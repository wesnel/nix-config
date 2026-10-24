package plans

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A plan can start from a template in the plans repository: the template's
// entries land in the same sections of the new plan.
func TestTemplate(t *testing.T) {
	// withTemplate makes a config whose plans repository holds template
	// release with text.
	withTemplate := func(t *testing.T, text string) Config {
		t.Helper()
		c := testConfig(t)
		dir := filepath.Join(c.repo(), "templates")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "release.org"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return c
	}
	release := "#+title: Release\n#+TODO: TODO STARTED | DONE VERIFIED ABANDONED\n\n" +
		"** Steps\n*** TODO Bump the version\n:PROPERTIES:\n:CUSTOM_ID: S1\n:END:\n*** TODO Tag it\n:PROPERTIES:\n:CUSTOM_ID: S2\n:AFTER: S1\n:END:\n" +
		"** Verification\n- CI passes on the tag.\n"

	t.Run("copied", func(t *testing.T) {
		c := withTemplate(t, release)
		made, err := c.New(NewOptions{Title: "Release 1.2", Slug: "release", Template: "release"}, day)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(made.File)
		text := string(data)
		if !strings.Contains(text, "** Steps\n*** TODO Bump the version\n:PROPERTIES:\n:CUSTOM_ID: S1\n:END:\n*** TODO Tag it\n") ||
			!strings.HasSuffix(text, "** Verification\n- CI passes on the tag.\n") ||
			!strings.Contains(text, "#+title: Release 1.2\n") {
			t.Errorf("plan:\n%s", text)
		}
		p, _ := c.Open(made.Plan)
		if s, err := p.Status(); err != nil || strings.Join(s.ReadySteps, " ") != "S1" {
			t.Errorf("ready steps %v, %v", s.ReadySteps, err)
		}
	})

	// builtIn gives c a built-in templates directory holding template
	// release with text.
	builtIn := func(t *testing.T, c Config, text string) Config {
		t.Helper()
		c.Templates = t.TempDir()
		if err := os.WriteFile(filepath.Join(c.Templates, "release.org"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return c
	}

	t.Run("built in", func(t *testing.T) {
		c := builtIn(t, testConfig(t), release)
		made, err := c.New(NewOptions{Title: "Release 1.2", Slug: "release", Template: "release"}, day)
		if err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(made.File); !strings.Contains(string(data), "*** TODO Bump the version\n") {
			t.Errorf("plan:\n%s", data)
		}
	})

	t.Run("the plans repository's before the built-in one", func(t *testing.T) {
		c := builtIn(t, withTemplate(t, release), strings.ReplaceAll(release, "Bump the version", "Built in"))
		made, err := c.New(NewOptions{Title: "Release 1.2", Slug: "release", Template: "release"}, day)
		if err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(made.File); !strings.Contains(string(data), "*** TODO Bump the version\n") {
			t.Errorf("plan:\n%s", data)
		}
	})

	t.Run("refused/a built-in template with drift", func(t *testing.T) {
		c := builtIn(t, testConfig(t), release+"** Follow-ups\n*** TODO Announce it\n")
		_, err := c.New(NewOptions{Title: "T", Slug: "x", Template: "release"}, day)
		isRefusal(t, "a built-in template with drift", err)
	})

	for what, c := range map[string][2]string{
		"an unknown section":        {"release", release + "** Rollout\n- slowly\n"},
		"a keyword outside steps'":  {"release", "#+TODO: TODO NEXT | DONE\n** Steps\n*** NEXT Bump it\n"},
		"a step without its ID":     {"release", release + "** Follow-ups\n*** TODO Announce it\n"},
		"a question":                {"release", release + "** Questions\n*** Ship it?\n:PROPERTIES:\n:CUSTOM_ID: Q1\n:END:\n"},
		"a step in the old form":    {"release", strings.Replace(release, ":CUSTOM_ID: S1", ":ID: S1", 1)},
		"a heading under Goal":      {"release", release + "** Goal\n*** Why\n"},
		"a missing template":        {"other", release},
		"a name that is not a name": {"../release", release},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			cfg := withTemplate(t, c[1])
			_, err := cfg.New(NewOptions{Title: "T", Slug: "x", Template: c[0]}, day)
			isRefusal(t, what, err)
			if entries, _ := os.ReadDir(cfg.Dir); len(entries) > 1 {
				t.Errorf("a refused plan left files: %v", entries)
			}
		})
	}
}
