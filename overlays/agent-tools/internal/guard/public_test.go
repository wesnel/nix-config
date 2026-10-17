package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The guard refuses a pull request's title or description, a comment or an
// API write that names what publictext finds, read where it can be.
func TestPublicText(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("leak.md", "Keeps the flag, as D188 settled.\n")
	write("good.md", "Keeps the flag; see AGENT-1191 and 1a2b3c4.\n")
	write("leak.json", `{"body": "Fixed with the other one (tZ1Cm)."}`)
	write("good.json", `{"body": "Fixed in 1a2b3c4."}`)
	write("leak.graphql", `mutation { addComment(input: {subjectId: "PR_kwDOAbc123", body: "Fixed per D188."}) { clientMutationId } }`)

	t.Run("judged", func(t *testing.T) {
		for _, c := range []struct{ command, token string }{
			{`gh pr create --draft --title T --body "Fixes the symlinked case (tZ1Cm)."`, "tZ1Cm"},
			{`gh pr create --draft --title "Fix the symlinked case (tZ1Cm)" --body-file good.md`, "tZ1Cm"},
			{"gh pr edit 1 --body-file leak.md", "D188"},
			{"gh pr edit 1 -F leak.md", "D188"},
			{"gh pr edit 1 --body-file=" + filepath.Join(dir, "leak.md"), "D188"},
			{"gh pr edit 1 -b D188", "D188"},
			{"cd " + dir + " && gh pr edit 1 --body-file leak.md", "D188"},
			{"gh pr edit 1 --title 'Notes in /tmp/claude-501/s'", "/tmp/claude-501/s"},
		} {
			reason := judge(t, dir, c.command)
			if !strings.Contains(reason, `"`+c.token+`"`) || !saysInstead(reason) {
				t.Errorf("%q: %q", c.command, reason)
			}
		}
		for _, command := range []string{
			`gh pr create --draft --title "Resolve symlinks first" --body "Fixes AGENT-1191; see 1a2b3c4."`,
			"gh pr edit 1 --body-file good.md",
			"gh pr view 1 --json body",
		} {
			if reason := judge(t, dir, command); reason != "" {
				t.Errorf("%q: %q", command, reason)
			}
		}
	})

	// Comments and API writes are the operator's in every session today;
	// their bodies are checked all the same.
	t.Run("bodies", func(t *testing.T) {
		for _, c := range []struct {
			args  []string
			token string
		}{
			{[]string{"pr", "comment", "1", "--body", "See (tZ1Cm)."}, "tZ1Cm"},
			{[]string{"pr", "review", "1", "--comment", "-b", "Per D188."}, "D188"},
			{[]string{"issue", "comment", "1", "--body-file", "leak.md"}, "D188"},
			{[]string{"api", "repos/o/r/issues/1/comments", "-f", "body=See (tZ1Cm)."}, "tZ1Cm"},
			{[]string{"api", "repos/o/r/issues/1/comments", "-fbody=See (tZ1Cm)."}, "tZ1Cm"},
			{[]string{"api", "repos/o/r/issues/1/comments", "--raw-field=body=See (tZ1Cm)."}, "tZ1Cm"},
			{[]string{"api", "repos/o/r/issues/1/comments", "-F", "body=@leak.md"}, "D188"},
			{[]string{"api", "repos/o/r/issues/1/comments", "--input", "leak.json"}, "tZ1Cm"},
			{[]string{"api", "graphql", "-F", "query=@leak.graphql"}, "D188"},
			{[]string{"api", "graphql", "-f", "query=mutation($b: String!) { x(body: $b) }", "-f", "b=x", "-f", "body=Per D188."}, "D188"},
		} {
			reason := checkPublicGh(c.args, make([]bool, len(c.args)), dir)
			if !strings.Contains(reason, `"`+c.token+`"`) || !saysInstead(reason) {
				t.Errorf("%q: %q", c.args, reason)
			}
		}
		for _, args := range [][]string{
			{"api", "repos/o/r/issues/1/comments", "-f", "body=Fixed in 1a2b3c4."},
			{"api", "repos/o/r/issues/1/comments", "--input", "good.json"},
			{"api", "graphql", "-f", "threadId=PRRT_kwDOKj0Wfs5tZ1Cm", "-f", "query=mutation($threadId: ID!) { resolveReviewThread(input: {threadId: $threadId}) { thread { id } } }"},
			{"pr", "comment", "1", "--body", "Fixed in 1a2b3c4 (AGENT-1191)."},
			// Text it cannot read is for other rules to judge.
			{"pr", "edit", "1", "--body-file", "missing.md"},
			{"pr", "edit", "1", "--body-file", "-"},
		} {
			if reason := checkPublicGh(args, make([]bool, len(args)), dir); reason != "" {
				t.Errorf("%q: %q", args, reason)
			}
		}
		if reason := checkPublicGh([]string{"pr", "edit", "1", "--body", "$BODY"}, []bool{false, false, false, false, true}, dir); reason != "" {
			t.Errorf("an expanded body: %q", reason)
		}
	})
}
