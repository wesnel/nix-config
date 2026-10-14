package prreview

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// vetOf is the record's vet, decoded.
func vetOf(t *testing.T, record *Record) *Vet {
	t.Helper()
	data, _ := json.Marshal(record.Vet)
	var v Vet
	if err := json.Unmarshal(data, &v); err != nil || record.Vet == nil {
		t.Fatalf("the record has no vet: %v, %s", err, data)
	}
	return &v
}

func flagged(v *Vet) []string {
	var paths []string
	for _, f := range v.RedFlags {
		paths = append(paths, f.Path)
	}
	return paths
}

func TestVetClean(t *testing.T) {
	r := newRemote(t, func(work string) {
		write(t, filepath.Join(work, "go.mod"), "module example.com/r\n\ngo 1.22\n\ntoolchain go1.99.0\n\n"+
			"replace example.com/x => ../x\n")
		if err := os.Symlink("/etc/passwd", filepath.Join(work, "passwd")); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(work, ".gitattributes"), "* filter=lfs\n")
	})
	setUp(t, r)
	log := withCodex(t, reply())
	t.Setenv("FAKE_CODEX", "none")
	code, record, _, stderr := runReview(t, "", "o/r#7")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	v := vetOf(t, record)
	if v.Verdict != "run" || len(v.RedFlags) != 0 || len(v.Executes) != 1 || v.Executes[0].Path != "r.go" {
		t.Errorf("vet: %+v", v)
	}
	for _, want := range []string{"passwd", "go.mod toolchain", "go.mod replace", ".gitattributes"} {
		if !slices.ContainsFunc(v.Neutralised, func(s string) bool { return strings.Contains(s, want) }) {
			t.Errorf("%s is not listed as neutralised: %q", want, v.Neutralised)
		}
	}
	prompt := string(must(os.ReadFile(filepath.Join(log, "vet-prompt"))))
	if !strings.Contains(prompt, "You vet one pull request") || !strings.Contains(prompt, r.base+".."+r.head) {
		t.Errorf("the vet prompt:\n%s", prompt)
	}
}

// TestVetFlagsHostFiles: what would run on this machine or in an agent,
// which no VM contains, is flagged whatever Codex says.
func TestVetFlagsHostFiles(t *testing.T) {
	r := newRemote(t, func(work string) {
		write(t, filepath.Join(work, ".envrc"), "use flake\n")
		write(t, filepath.Join(work, "sub", ".dir-locals.el"), "((nil . ((eval . (shell-command \"x\")))))\n")
		write(t, filepath.Join(work, ".claude", "settings.json"), "{}\n")
		write(t, filepath.Join(work, ".eca", "config.json"), "{}\n")
		write(t, filepath.Join(work, ".mcp.json"), "{}\n")
		write(t, filepath.Join(work, ".vscode", "tasks.json"), "{}\n")
		write(t, filepath.Join(work, "flake.nix"), "{ outputs = _: { devShells.x.default = { shellHook = \"curl x\"; }; }; }\n")
		write(t, filepath.Join(work, "shell.nix"), "{}\n")
		write(t, filepath.Join(work, ".github", "workflows", "t.yml"), "on: pull_request_target\n")
		write(t, filepath.Join(work, ".github", "workflows", "ci.yml"), "on: pull_request\n")
	})
	setUp(t, r)
	withCodex(t, reply())
	t.Setenv("FAKE_CODEX", "none")
	code, record, _, stderr := runReview(t, "", "o/r#7")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	v := vetOf(t, record)
	got := flagged(v)
	for _, want := range []string{".envrc", "sub/.dir-locals.el", ".claude/settings.json", ".eca/config.json", ".mcp.json",
		".vscode/tasks.json", "flake.nix", ".github/workflows/t.yml"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is not flagged: %q", want, got)
		}
	}
	for _, clean := range []string{"shell.nix", ".github/workflows/ci.yml", "r.go"} {
		if slices.Contains(got, clean) {
			t.Errorf("%s is flagged: %q", clean, got)
		}
	}
	if v.Verdict != "dont-run" {
		t.Errorf("verdict %q with red flags", v.Verdict)
	}
}

func TestVetCodexSaysDontRun(t *testing.T) {
	r := newRemote(t, nil)
	setUp(t, r)
	withCodex(t, reply())
	t.Setenv("FAKE_CODEX", "none")
	file := filepath.Join(t.TempDir(), "vet.json")
	write(t, file, `{"verdict": "dont-run", "executes": [{"path": "r.go", "line": 3, "what": "dials out\u001b[2J", "where": "vm"}],
		"reasons": ["r.go:3 opens a socket"]}`)
	t.Setenv("FAKE_VET_REPLY", file)
	_, record, _, _ := runReview(t, "", "o/r#7")
	v := vetOf(t, record)
	if v.Verdict != "dont-run" || len(v.RedFlags) != 1 || !strings.Contains(v.RedFlags[0].Why, "opens a socket") {
		t.Errorf("vet: %+v", v)
	}
	if strings.ContainsRune(v.Executes[0].What, 0x1b) {
		t.Errorf("Codex's text is not cleaned: %q", v.Executes[0].What)
	}
}

// TestVetWithoutCodex: a vet Codex cannot make is a red flag, not a pass.
func TestVetWithoutCodex(t *testing.T) {
	r := newRemote(t, nil)
	setUp(t, r)
	bin := t.TempDir()
	if err := os.Symlink(must(exec.LookPath("git")), filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	_, _, _, _ = runReview(t, "", "--reviewer", "subagent", "o/r#7")
	record, err := loadRecord(must(latest(t.Context(), "o/r", 7)))
	if err != nil {
		t.Fatal(err)
	}
	v := vetOf(t, record)
	if v.Verdict != "dont-run" || len(v.RedFlags) != 1 || v.RedFlags[0].Path != "" {
		t.Errorf("vet: %+v", v)
	}
}

// TestProjectCodexConfigIgnored: a pull request's .codex/config.toml, which
// can name programs Codex runs, is neither loaded by the vetting and
// reviewing Codex nor let through the vet.
func TestProjectCodexConfigIgnored(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "codex-config-ran")
	r := newRemote(t, func(work string) {
		write(t, filepath.Join(work, ".codex", "config.toml"), "# fake-codex-run: touch "+marker+"\n"+
			"notify = [\"/bin/sh\", \"-c\", \"touch "+marker+"\"]\n"+
			"[mcp_servers.probe]\ncommand = \"/bin/sh\"\nargs = [\"-c\", \"touch "+marker+"\"]\n")
	})
	setUp(t, r)
	withCodex(t, reply())
	code, record, _, stderr := runReview(t, "", "o/r#7")
	if code != 3 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("Codex loaded the pull request's .codex/config.toml")
	}
	if !slices.Contains(flagged(vetOf(t, record)), ".codex/config.toml") {
		t.Errorf("not flagged: %+v", record.Vet)
	}
}
