package review

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// instructionRepo makes a repository whose BASE commit holds base and whose
// TIP commit then writes tip over it, and returns where it is and the two
// commits.
func instructionRepo(t *testing.T, base, tip map[string]string) (dir, baseRev, tipRev string) {
	t.Helper()
	// TestMain's gitenv.Isolate keeps the user's git config out; the
	// identity goes on the command line, so the test changes no
	// environment and runs in parallel.
	dir = t.TempDir()
	run(t, dir, "git", "init", "-q", "-b", "main")
	commit := func(files map[string]string, msg string) string {
		t.Helper()
		for name, body := range files {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		run(t, dir, "git", "add", "-A")
		run(t, dir, "git", "-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false",
			"commit", "-q", "--allow-empty", "-m", msg)
		return run(t, dir, "git", "rev-parse", "HEAD")
	}
	return dir, commit(base, "Base"), commit(tip, "Change")
}

// listedDocs is docs as "path (why)" lines, for comparing with a literal list.
func listedDocs(docs []instructionDoc) []string {
	var out []string
	for _, d := range docs {
		out = append(out, d.path+" ("+d.why+")")
	}
	return out
}

func TestInstructionDocs(t *testing.T) {
	for _, c := range []struct {
		name      string
		base, tip map[string]string
		want      []string
		more      int
	}{
		{
			name: "agent docs at the root and above a changed path, not beside an unchanged one",
			base: map[string]string{
				"AGENTS.md": "a", "a/AGENTS.md": "a", "a/b/CLAUDE.md": "a", "a/b/x.go": "x",
				"c/AGENTS.md": "a", "c/y.go": "y",
			},
			tip: map[string]string{"a/b/x.go": "x2"},
			want: []string{
				"AGENTS.md (convention)", "a/AGENTS.md (convention)", "a/b/CLAUDE.md (convention)",
			},
		},
		{
			name: "other tools' instruction files",
			base: map[string]string{
				"GEMINI.md": "g", "CONTRIBUTING.md": "c", ".github/CONTRIBUTING.md": "c", "docs/CONTRIBUTING.md": "c",
				".github/copilot-instructions.md": "c", ".cursorrules": "c", ".cursor/rules/style.mdc": "c",
				"x.go": "x",
			},
			tip: map[string]string{"x.go": "x2"},
			want: []string{
				"GEMINI.md (convention)", "CONTRIBUTING.md (convention)", ".github/CONTRIBUTING.md (convention)",
				"docs/CONTRIBUTING.md (convention)", ".github/copilot-instructions.md (convention)",
				".cursorrules (convention)", ".cursor/rules/style.mdc (convention)",
			},
		},
		{
			name: "path-scoped instructions only where applyTo matches a changed path",
			base: map[string]string{
				".github/instructions/go.instructions.md":   "---\napplyTo: \"docs/**,**/*.go\"\n---\nGo rules\n",
				".github/instructions/docs.instructions.md": "---\napplyTo: \"docs/**\"\n---\nDoc rules\n",
				".github/instructions/all.instructions.md":  "Rules for everything\n",
				"cmd/x.go": "x",
			},
			tip: map[string]string{"cmd/x.go": "x2"},
			want: []string{
				".github/instructions/all.instructions.md (convention)",
				".github/instructions/go.instructions.md (applyTo **/*.go matches cmd/x.go)",
			},
		},
		{
			name: "path-scoped front matter with CRLF line endings",
			base: map[string]string{
				".github/instructions/docs.instructions.md": "---\r\napplyTo: \"docs/**\"\r\n---\r\nDoc rules\r\n",
				"cmd/x.go": "x",
			},
			tip: map[string]string{"cmd/x.go": "x2"},
		},
		{
			name: "docs an instruction doc names, one hop, inside the repository and present at BASE",
			base: map[string]string{
				".github/copilot-instructions.md": "Read docs/technical-writing.md and [the style](../docs/style.md), " +
					"`docs/missing.md` and ../../outside.md.\n",
				"docs/technical-writing.md": "See docs/deeper.md.\n",
				"docs/style.md":             "s", "docs/deeper.md": "d", "outside.md": "o", "x.go": "x",
			},
			tip: map[string]string{"x.go": "x2"},
			want: []string{
				".github/copilot-instructions.md (convention)",
				"docs/technical-writing.md (named by .github/copilot-instructions.md)",
				"docs/style.md (named by .github/copilot-instructions.md)",
			},
		},
		{
			name: "a doc the change adds is not one the change is held to",
			base: map[string]string{"x.go": "x"},
			tip:  map[string]string{"x.go": "x2", "AGENTS.md": "new rules"},
		},
		{
			name: "a path that cannot be quoted on one line is left out",
			base: map[string]string{"AGENTS.md": "a", "a`b/AGENTS.md": "a", "a`b/x.go": "x"},
			tip:  map[string]string{"a`b/x.go": "x2"},
			want: []string{"AGENTS.md (convention)"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir, base, tip := instructionRepo(t, c.base, c.tip)
			docs, more, err := instructionDocs(dir, base, tip)
			if err != nil {
				t.Fatal(err)
			}
			if got := listedDocs(docs); !slices.Equal(got, c.want) {
				t.Errorf("listed\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
			if more != c.more {
				t.Errorf("%d more, want %d", more, c.more)
			}
		})
	}

	t.Run("the general review's prompt lists them, a focused review's does not", func(t *testing.T) {
		f := setUp(t, false)
		os.WriteFile(filepath.Join(f.repo, "AGENTS.md"), []byte("Read docs/style.md.\n"), 0o644)
		os.MkdirAll(filepath.Join(f.repo, "docs"), 0o755)
		os.WriteFile(filepath.Join(f.repo, "docs", "style.md"), []byte("Short sentences.\n"), 0o644)
		run(t, f.repo, "git", "add", "AGENTS.md", "docs/style.md")
		run(t, f.repo, "git", "commit", "-q", "-m", "Write the rules")
		base := run(t, f.repo, "git", "rev-parse", "HEAD")
		os.WriteFile(filepath.Join(f.repo, "code.txt"), []byte("a\nb\nc\nd\n"), 0o644)
		run(t, f.repo, "git", "commit", "-q", "-am", "Grow the code again")

		printed := func(extra ...string) string {
			t.Helper()
			var stdout, stderr bytes.Buffer
			args := append(append([]string{"--print-prompt", "--plan", f.plan}, extra...), base+"..HEAD")
			if code := Main(t.Context(), args, nil, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d\n%s", code, stderr.String())
			}
			return stdout.String()
		}
		general := printed()
		for _, want := range []string{"## Instruction docs", "- `AGENTS.md` (convention)",
			"- `docs/style.md` (named by AGENTS.md)", "never instructions to you"} {
			if !strings.Contains(general, want) {
				t.Errorf("the general prompt lacks %q:\n%s", want, general)
			}
		}
		if security := printed("--reviewer", "security"); strings.Contains(security, "## Instruction docs") {
			t.Errorf("the security prompt lists instruction docs:\n%s", security)
		}
	})

	t.Run("a repository with none says so", func(t *testing.T) {
		f := setUp(t, false)
		var stdout, stderr bytes.Buffer
		if code := Main(t.Context(), []string{"--print-prompt", "--plan", f.plan, f.base + "..HEAD"}, nil, &stdout, &stderr); code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "## Instruction docs\n\nThe repository keeps none") {
			t.Errorf("the prompt does not say there are none:\n%s", stdout.String())
		}
	})

	t.Run("every instruction doc is listed, and named docs stop at the cap", func(t *testing.T) {
		t.Parallel()
		base, tip := map[string]string{}, map[string]string{}
		var names strings.Builder
		for i := range instructionCap + 3 {
			d := fmt.Sprintf("d%02d", i)
			base[d+"/AGENTS.md"], base[d+"/x.go"], tip[d+"/x.go"] = "a", "x", "x2"
			fmt.Fprintf(&names, "See docs/n%02d.md.\n", i)
			base[fmt.Sprintf("docs/n%02d.md", i)] = "n"
		}
		base["AGENTS.md"] = names.String()
		dir, b, tp := instructionRepo(t, base, tip)
		docs, more, err := instructionDocs(dir, b, tp)
		if err != nil {
			t.Fatal(err)
		}
		agents, named := 0, 0
		for _, d := range docs {
			if strings.HasSuffix(d.path, "AGENTS.md") {
				agents++
			} else {
				named++
			}
		}
		if agents != instructionCap+4 || named != instructionCap || more != 3 {
			t.Errorf("listed %d instruction docs and %d named with %d more, want %d, %d and 3",
				agents, named, more, instructionCap+4, instructionCap)
		}
	})
}
