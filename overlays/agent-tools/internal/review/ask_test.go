package review

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// askCodex runs `git fresh-review ask` with args, and returns its exit
// status, its answer and stderr.
func askCodex(t *testing.T, args ...string) (int, map[string]any, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Main(t.Context(), append([]string{"ask"}, args...), strings.NewReader(""), &stdout, &stderr)
	var out map[string]any
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
		}
	}
	return code, out, stderr.String()
}

// askFixture makes the repository setUp makes, with a design note in a
// subdirectory and a question beside the repository's files.
func askFixture(t *testing.T, withCodex bool) *fixture {
	t.Helper()
	f := setUp(t, withCodex)
	os.MkdirAll(filepath.Join(f.repo, "docs"), 0o755)
	os.WriteFile(filepath.Join(f.repo, "docs", "design.md"), []byte("Two ways to add the flag.\n"), 0o644)
	os.WriteFile(filepath.Join(f.repo, ".git", "question.md"), []byte("Which way keeps the flag in one place?\n"), 0o644)
	return f
}

// Codex answers a design question from the files named and nothing else,
// confined as a review is, and leaves nothing behind.
func TestAsk(t *testing.T) {
	t.Run("answers from the named files alone", func(t *testing.T) {
		f := askFixture(t, true)
		t.Setenv("FAKE_CODEX", "answer")
		code, out, stderr := askCodex(t, "--question", ".git/question.md", "code.txt", "docs/design.md")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		if out["answer"] != "Extend the seam." || out["reviewer"] != "codex" || out["model"] != "fake-model" {
			t.Errorf("answer %v", out)
		}
		if u, _ := out["unverified"].([]any); len(u) != 1 || u[0] != "the callers" {
			t.Errorf("unverified %v", out["unverified"])
		}
		if files := strings.Fields(string(must(os.ReadFile(filepath.Join(f.log, "files"))))); strings.Join(files, " ") != ".git code.txt docs" {
			t.Errorf("Codex saw %v, not the named files alone", files)
		}
		prompt := string(must(os.ReadFile(filepath.Join(f.log, "prompt"))))
		for _, want := range []string{"Which way keeps the flag in one place?", "- code.txt", "- docs/design.md"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("the prompt lacks %q:\n%s", want, prompt)
			}
		}
		args := string(must(os.ReadFile(filepath.Join(f.log, "exec-args"))))
		for _, want := range []string{"--ignore-user-config", `web_search="disabled"`, `default_permissions="fresh-review"`} {
			if !strings.Contains(args, want) {
				t.Errorf("Codex ran without %s:\n%s", want, args)
			}
		}
		f.checkCleanedUp(t)
	})

	t.Run("Codex unavailable", func(t *testing.T) {
		f := askFixture(t, false)
		code, _, stderr := askCodex(t, "--question", ".git/question.md", "code.txt")
		reason := strings.TrimPrefix(strings.TrimSpace(stderr), "git-fresh-review: ")
		if code != 4 || !strings.Contains(reason, "sub-agent") || !refusal.SaysInstead(reason) {
			t.Errorf("exit %d, %q", code, stderr)
		}
		f.checkCleanedUp(t)
	})

	// Known secret shapes are taken out of the question and of each file
	// copied for Codex.
	t.Run("redacted", func(t *testing.T) {
		f := askFixture(t, true)
		t.Setenv("FAKE_CODEX", "answer")
		t.Setenv("FAKE_CODEX_COPY", "docs/design.md")
		token := "ghp_" + strings.Repeat("c", 36)
		os.WriteFile(filepath.Join(f.repo, ".git", "question.md"), []byte("Is "+token+" safe here?\n"), 0o644)
		os.WriteFile(filepath.Join(f.repo, "docs", "design.md"), []byte("The token is "+token+".\n"), 0o644)
		if code, _, stderr := askCodex(t, "--question", ".git/question.md", "docs/design.md"); code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		for _, sent := range []string{"prompt", "copy"} {
			text := string(must(os.ReadFile(filepath.Join(f.log, sent))))
			if strings.Contains(text, token) || !strings.Contains(text, "[REDACTED") {
				t.Errorf("the %s keeps the token:\n%s", sent, text)
			}
		}
		f.checkCleanedUp(t)
	})

	// The question goes to Codex with only known secret shapes taken out,
	// so it comes from the repository or the scratch area, read where its
	// symlinks lead, and never from a credential.
	t.Run("question files", func(t *testing.T) {
		for _, c := range []struct {
			name string
			// place makes the question file and returns the path to pass;
			// scratch is the scratch area and secrets a credential prefix.
			place func(t *testing.T, f *fixture, scratch, secrets string) string
			ok    bool
		}{
			{"in the repository", func(t *testing.T, f *fixture, _, _ string) string {
				return ".git/question.md"
			}, true},
			{"in the scratch area", func(t *testing.T, _ *fixture, scratch, _ string) string {
				q := filepath.Join(scratch, "q.md")
				os.WriteFile(q, []byte("Which way?\n"), 0o644)
				return q
			}, true},
			{"elsewhere", func(t *testing.T, _ *fixture, _, _ string) string {
				q := filepath.Join(t.TempDir(), "q.md")
				os.WriteFile(q, []byte("Which way?\n"), 0o644)
				return q
			}, false},
			{"a credential in the scratch area", func(t *testing.T, _ *fixture, _, secrets string) string {
				q := filepath.Join(secrets, "token")
				os.WriteFile(q, []byte("hunter2\n"), 0o600)
				return q
			}, false},
			{"a link in the repository to a file elsewhere", func(t *testing.T, f *fixture, _, _ string) string {
				q := filepath.Join(t.TempDir(), "q.md")
				os.WriteFile(q, []byte("Which way?\n"), 0o644)
				os.Symlink(q, filepath.Join(f.repo, ".git", "link.md"))
				return ".git/link.md"
			}, false},
			{"a link in the scratch area to a credential", func(t *testing.T, _ *fixture, scratch, secrets string) string {
				os.WriteFile(filepath.Join(secrets, "token"), []byte("hunter2\n"), 0o600)
				os.Symlink(filepath.Join(secrets, "token"), filepath.Join(scratch, "q.md"))
				return filepath.Join(scratch, "q.md")
			}, false},
		} {
			t.Run(c.name, func(t *testing.T) {
				f := askFixture(t, true)
				t.Setenv("FAKE_CODEX", "answer")
				scratch, _ := filepath.EvalSymlinks(t.TempDir())
				secrets := filepath.Join(scratch, "secrets")
				os.MkdirAll(secrets, 0o700)
				old := scratchRoots
				scratchRoots = func() []string { return []string{scratch} }
				t.Cleanup(func() { scratchRoots = old })
				config := filepath.Join(t.TempDir(), "config.json")
				data, _ := json.Marshal(map[string]any{"guard": map[string]any{"credentialPaths": []string{secrets}}})
				os.WriteFile(config, data, 0o600)
				t.Setenv("AGENT_TOOLS_CONFIG", config)
				code, _, stderr := askCodex(t, "--question", c.place(t, f, scratch, secrets), "code.txt")
				_, ran := os.Stat(filepath.Join(f.log, "prompt"))
				if c.ok && code != 0 {
					t.Errorf("exit %d\n%s", code, stderr)
				}
				if !c.ok {
					reason := strings.TrimPrefix(strings.TrimSpace(stderr), "git-fresh-review: refused: ")
					if code != 2 || !refusal.SaysInstead(reason) || ran == nil {
						t.Errorf("exit %d, Codex ran: %v, %q", code, ran == nil, stderr)
					}
				}
				f.checkCleanedUp(t)
			})
		}
	})

	for name, args := range map[string][]string{
		"no question":                   {"code.txt"},
		"no file":                       {"--question", ".git/question.md"},
		"a file outside the repository": {"--question", ".git/question.md", "/etc/hosts"},
		"a missing file":                {"--question", ".git/question.md", "gone.txt"},
		"a directory":                   {"--question", ".git/question.md", "docs"},
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			f := askFixture(t, true)
			t.Setenv("FAKE_CODEX", "answer")
			code, _, stderr := askCodex(t, args...)
			reason := strings.TrimPrefix(strings.TrimSpace(stderr), "git-fresh-review: refused: ")
			if code != 2 || !refusal.SaysInstead(reason) {
				t.Errorf("%v: exit %d, %q", args, code, stderr)
			}
			if _, err := os.Stat(filepath.Join(f.log, "prompt")); err == nil {
				t.Error("Codex ran")
			}
			f.checkCleanedUp(t)
		})
	}
}
