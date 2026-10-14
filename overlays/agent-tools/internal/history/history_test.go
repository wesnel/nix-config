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
	for message, want := range map[string]struct {
		kind   Kind
		target string
	}{
		"A first":                        {Kept, ""},
		"fixup!A first":                  {Kept, ""},
		"fixup! A first\n\nbody":         {Fixup, "A first"},
		"amend! A first\n\nA new":        {Amend, "A first"},
		"squash! A first":                {Squash, "A first"},
		"fixup! fixup! A first":          {Fixup, "A first"},
		"fixup! amend! A first\n\nbody":  {Fixup, "A first"},
		"squash! fixup! A first":         {Squash, "A first"},
		"amend! squash! fixup! A first":  {Amend, "A first"},
		"A first mentions fixup! itself": {Kept, ""},
	} {
		if kind, target := Fold(message); kind != want.kind || target != want.target {
			t.Errorf("Fold(%q) = %v %q, want %v %q", message, kind, target, want.kind, want.target)
		}
	}
}

func TestLintMessage(t *testing.T) {
	long := strings.Repeat("x", 73)
	for _, c := range []struct{ message, want string }{
		{"Add the run loop", ""},
		{"Add the run loop\n\nIt runs.", ""},
		{strings.Repeat("é", 72), ""},
		{long, "subject is 73 characters, over 72"},
		{"feat(loop): " + strings.Repeat("x", 61), "subject is 73 characters, over 72"},
		{"Add the run loop.", "subject ends with a period"},
		{"Add x\n\nWhy.\n\nCo-Authored-By: C <c@e>", `"Co-Authored-By: C <c@e>" is attribution`},
		{"Add x\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)", "is attribution"},
		{"Add x\n\nhttps://claude.ai/code/session_01ab", "is attribution"},
		{"Add x\n\nhttps://chatgpt.com/codex/tasks/task_e_1", "is attribution"},
		{"Add x\n\nSigned-off-by: T <t@e>", ""},
		{"Add x\n\nThe table is generated with go generate.", ""},
		{"fixup! " + long, ""},
		{"fixup! A\n\nReview-Thread: x\nReview-Finding: y", ""},
		{"fixup! A\n\nReview-Thread: x\nSigned-off-by: T <t@e>", `trailer "Signed-off-by" is not a Review-* trailer`},
		{"fixup! A\n\nCo-authored-by: C <c@e>", "is attribution"},
		{"amend! A\n\n" + long, ""},
	} {
		got := strings.Join(LintMessage(c.message), "; ")
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("LintMessage(%q) = %q, want %q", c.message, got, c.want)
		}
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
			defer func() { git.Dir = "" }()
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
