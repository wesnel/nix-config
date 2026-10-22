package history

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
)

func TestFold(t *testing.T) {
	for _, c := range []struct {
		name, message string
		kind          Kind
		target        string
	}{
		{"plain subject", "A first", Kept, ""},
		{"prefix without a space", "fixup!A first", Kept, ""},
		{"fixup", "fixup! A first\n\nbody", Fixup, "A first"},
		{"amend", "amend! A first\n\nA new", Amend, "A first"},
		{"squash", "squash! A first", Squash, "A first"},
		{"fixup of a fixup", "fixup! fixup! A first", Fixup, "A first"},
		{"fixup of an amend", "fixup! amend! A first\n\nbody", Fixup, "A first"},
		{"squash of a fixup", "squash! fixup! A first", Squash, "A first"},
		{"amend of a squash of a fixup", "amend! squash! fixup! A first", Amend, "A first"},
		{"prefix inside the subject", "A first mentions fixup! itself", Kept, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if kind, target := Fold(c.message); kind != c.kind || target != c.target {
				t.Errorf("Fold(%q) = %v %q, want %v %q", c.message, kind, target, c.kind, c.target)
			}
		})
	}
}

func TestLintMessage(t *testing.T) {
	long := strings.Repeat("x", 73)
	for _, c := range []struct{ name, message, want string }{
		{"subject only", "Add the run loop", ""},
		{"subject and body", "Add the run loop\n\nIt runs.", ""},
		{"72 characters of two bytes each", strings.Repeat("é", 72), ""},
		{"73 characters", long, "subject is 73 characters, over 72"},
		{"73 characters with a type prefix", "feat(loop): " + strings.Repeat("x", 61), "subject is 73 characters, over 72"},
		{"trailing period", "Add the run loop.", "subject ends with a period"},
		{"Co-Authored-By trailer", "Add x\n\nWhy.\n\nCo-Authored-By: C <c@e>", `"Co-Authored-By: C <c@e>" is attribution`},
		{"generated-with line", "Add x\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)", "is attribution"},
		{"Claude session link", "Add x\n\nhttps://claude.ai/code/session_01ab", "is attribution"},
		{"Codex task link", "Add x\n\nhttps://chatgpt.com/codex/tasks/task_e_1", "is attribution"},
		{"Signed-off-by trailer", "Add x\n\nSigned-off-by: T <t@e>", ""},
		{"generated in prose", "Add x\n\nThe table is generated with go generate.", ""},
		{"long fixup subject", "fixup! " + long, ""},
		{"fixup with Review trailers", "fixup! A\n\nReview-Thread: x\nReview-Finding: y", ""},
		{"fixup with another trailer", "fixup! A\n\nReview-Thread: x\nSigned-off-by: T <t@e>", `trailer "Signed-off-by" is not a Review-* trailer`},
		{"fixup with attribution", "fixup! A\n\nCo-authored-by: C <c@e>", "is attribution"},
		{"long amend body", "amend! A\n\n" + long, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(LintMessage(c.message), "; ")
			if c.want == "" && got != "" || !strings.Contains(got, c.want) {
				t.Errorf("LintMessage(%q) = %q, want %q", c.message, got, c.want)
			}
		})
	}
}

// TestSquashed runs git's own autosquash over each history and checks
// Squashed predicts the commits it leaves, messages and authors included.
func TestSquashed(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	// GIT_EDITOR=false fails any commit that opens an editor, so NoEditor
	// must override it.
	t.Setenv("GIT_EDITOR", "false")
	t.Setenv("GIT_SEQUENCE_EDITOR", "false")
	type commit struct{ message, author string }
	orig := "Orig <o@e>"
	for name, history := range map[string][]commit{
		"fixup":             {{"A1\n\nA body", orig}, {"fixup! A1\n\ndropped", ""}},
		"reword":            {{"A1\n\nA body\n\nCo-Authored-By: X <x@e>", orig}, {"amend! A1\n\nA1\n\nA body", ""}},
		"amend new subject": {{"A1\n\nA body", orig}, {"B1", ""}, {"amend! A1\n\nA renamed\n\nnew body", ""}},
		"squash":            {{"A1\n\nA body", orig}, {"squash! A1\n\nmore", ""}},
		"squash no body":    {{"A1\n\nA body", orig}, {"squash! A1", ""}},
		"amend then amend":  {{"A1", orig}, {"amend! A1\n\nFirst", ""}, {"amend! A1\n\nSecond", ""}},
		"squash then amend": {{"A1\n\nA body", orig}, {"squash! A1\n\nsq", ""}, {"amend! A1\n\nAm", ""}},
		"amend then squash": {{"A1\n\nA body", orig}, {"amend! A1\n\nAm", ""}, {"squash! A1\n\nsq", ""}},
		"squash then fixup": {{"A1\n\nA body", orig}, {"squash! A1\n\nsq", ""}, {"fixup! A1\n\ndropped", ""}},
		"nested":            {{"A1\n\nA body", orig}, {"fixup! amend! A1\n\ndropped", ""}, {"squash! fixup! A1\n\nsq", ""}},
		"two targets":       {{"A1", orig}, {"B1", orig}, {"amend! B1\n\nB2", ""}, {"squash! A1\n\nsq", ""}},
		"same subject":      {{"X\n\nfirst", orig}, {"X\n\nsecond", ""}, {"amend! X\n\nX new", ""}},
		"no target":         {{"A1", orig}, {"squash! gone\n\nsq", ""}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			run := func(env []string, args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), append([]string{"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@e"}, env...)...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return string(out)
			}
			run(nil, "init", "-q", "-b", "main")
			run([]string{"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@e"}, "commit", "-q", "--allow-empty", "-m", "base")
			run(nil, "switch", "-qc", "br")
			var commits []Commit
			for i, c := range history {
				author := c.author
				if author == "" {
					author = "T <t@e>"
				}
				name, email, _ := strings.Cut(strings.TrimSuffix(author, ">"), " <")
				file := fmt.Sprintf("f%d", i)
				os.WriteFile(filepath.Join(dir, file), nil, 0o644)
				run(nil, "add", file)
				run([]string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email}, "commit", "-q", "--cleanup=verbatim", "-m", c.message)
				commits = append(commits, Commit{Message: c.message, Author: author})
			}
			run(NoEditor, "rebase", "-q", "-i", "--autosquash", "--force-rebase", "main")
			git.Dir = dir
			t.Cleanup(func() { git.Dir = "" })
			got, err := Between("main", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			want := Squashed(commits)
			if len(got) != len(want) {
				t.Fatalf("git left %q, Squashed predicts %q", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("commit %d: git left %q, Squashed predicts %q", i, got[i], want[i])
				}
			}
		})
	}
}
