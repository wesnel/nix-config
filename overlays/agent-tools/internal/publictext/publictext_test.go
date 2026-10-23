package publictext

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

const thread = "PRRT_kwDOKj0Wfs5tZ1Cm"

func tokens(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Token)
	}
	return out
}

func TestCheck(t *testing.T) {
	session := "0b6c2f3e-1111-2222-3333-444455556666"
	for _, c := range []struct {
		name string
		text string
		ctx  Context
		want []string
	}{
		{"a thread's short key, as posted", "This came from the fix for the symlinked `~/.local` case (tZ1Cm), which predates this PR.",
			Context{}, []string{"tZ1Cm"}},
		{"a short key of a thread the caller knows", "Same cause as tZ1Cm: fixed in 1a2b3c4.",
			Context{Threads: []string{thread}}, []string{"tZ1Cm"}},
		{"a short key of digits and letters alone", "Fixed with the other one (tZqCm).", Context{}, []string{"tZqCm"}},
		{"short keys in a list", "Both fixed [tZ1Cm, aB3dE].", Context{}, []string{"tZ1Cm", "aB3dE"}},
		{"a short key after thread", "As in thread tZ1Cm, the path is resolved first.", Context{}, []string{"tZ1Cm"}},
		{"a thread's node ID", "Answered in " + thread + ".", Context{}, []string{thread}},
		{"a comment's node ID", "See PRRC_kwDOKj0Wfs6Abcde and IC_kwDOAbc123xyz.", Context{},
			[]string{"PRRC_kwDOKj0Wfs6Abcde", "IC_kwDOAbc123xyz"}},
		{"a legacy node ID", "Duplicate of MDExOlB1bGxSZXF1ZXN0MTIzNDU2.", Context{}, []string{"MDExOlB1bGxSZXF1ZXN0MTIzNDU2"}},
		{"a decision", "Per D188 this stays as it is.", Context{}, []string{"D188"}},
		{"a question in a part", "P1.Q3 covers the rest; see Q45.", Context{}, []string{"P1.Q3", "Q45"}},
		{"a question of the known plan", "As Q4 settled, the flag stays.", Context{PlanIDs: []string{"Q4"}}, []string{"Q4"}},
		{"loop vocabulary", "pr-loop pushed this in round 3, after a sub-agent fixed it.", Context{},
			[]string{"pr-loop", "round 3", "sub-agent"}},
		{"the loop's roles", "The coordinator agent overruled the owner sub-agent.", Context{},
			[]string{"coordinator agent", "owner sub-agent"}},
		{"review receipts and routes", "The review receipt is written; the review route is owed; fresh-review found nothing.", Context{},
			[]string{"review receipt", "review route", "fresh-review"}},
		{"vocabulary in code", "Ran `pr-loop reply` here.", Context{}, []string{"pr-loop"}},
		{"a scratch path", "Notes are in /tmp/claude-501/abc/scratchpad/notes.md.", Context{},
			[]string{"/tmp/claude-501/abc/scratchpad/notes.md"}},
		{"TMPDIR", "Wrote it to `$TMPDIR/reply.md` first.", Context{}, []string{"$TMPDIR/reply.md"}},
		{"the loop's state", "Its state is in .git/pr-loop/state.json, and the worktree under .pr-loop/feat.", Context{},
			[]string{".git/pr-loop/state.json", ".pr-loop/feat"}},
		{"the harness's config", "Set it in ~/.claude/settings.json.", Context{}, []string{"~/.claude/settings.json"}},
		{"a known local path", "Logged in /Users/someone/git/notes/agents/x.org.", Context{Paths: []string{"/Users/someone/git/notes/agents"}},
			[]string{"/Users/someone/git/notes/agents/x.org"}},
		{"the session", "From " + session + ".", Context{Sessions: []string{session}}, []string{session}},
		{"a session ID", "Seen in session " + session + ".", Context{}, []string{session}},
		{"a web session", "Seen in session_01AbCdEfGhIjKlMn.", Context{}, []string{"session_01AbCdEfGhIjKlMn"}},
		{"the harness's own repository keeps its IDs out", "Serve pr-loop to sub-agents (tZ1Cm) per D188.", Context{Harness: true},
			[]string{"tZ1Cm", "D188"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := tokens(Check(c.text, c.ctx)); !slices.Equal(got, c.want) {
				t.Errorf("Check(%q) = %q, want %q", c.text, got, c.want)
			}
		})
	}
}

// Replies a reviewer would write, which name code, commits, tickets and
// links, pass.
func TestCheckPasses(t *testing.T) {
	ctx := Context{Threads: []string{thread}, PlanIDs: []string{"S2", "F1", "P1"}}
	for _, text := range []string{
		"Fixed in 1a2b3c4: `resolveSymlinks` now calls `filepath.EvalSymlinks` before comparing, so a symlinked `~/.local` resolves to its target.",
		"Good catch. This predates the change (it reproduces on `main`); tracked as AGENT-1191.",
		"See https://github.com/ngrok/ngrok-agent/pull/721#discussion_r4300609600 for the earlier discussion.",
		"The D3D backend and the Q4 earnings report aren't affected, nor is #D12.",
		"Rounding errors are handled; a round trip through JSON keeps the value, and the 2nd round of retries backs off.",
		"Set `GOFLAGS=-mod=mod` and run `go test ./...`.",
		"Pinned to 9f8e7d6c5b4a39281706f5e4d3c2b1a098765432.",
		"The coordinator in the raft package picks a leader; its owner is the leader.",
		"Supports macOS (Darwin) and Linux, HTTP/2 (h2c) too, and the cipher is AES-GCM (aes256).",
		"Dev runs on r8a.2xlarge (64GiB), and the old one on (iOS17).",
		"The MCP servers are listed in ~/.claude.json.",
		"The socket is at macOS $TMPDIR/ngrok/ngrok.sock, and \\$TMPDIR/ngrok.sock before.",
		"Bumps golang.org/x/net to v0.33.0 (CVE-2024-45338).",
		"The fixture's ID, 123e4567-e89b-12d3-a456-426614174000, is arbitrary.",
		"CI fails in /home/runner/work/x/x/main.go and /usr/local/bin/git is old.",
		"Q1 2026's roadmap has it; S2 and F1 of the spec are unchanged, as is P1.",
		"The test now covers the thread above (Fixed).",
		"Both commits, a1b2c3d (Fix it) and e4f5a6b, are squashed.",
		"```\nPRRT_ appears in GraphQL as a prefix\n```",
	} {
		if got := Check(text, ctx); len(got) > 0 {
			t.Errorf("Check(%q) = %q, want nothing", text, tokens(got))
		}
	}
}

func TestRefusal(t *testing.T) {
	got := Refusal("the reply", Check("Fixed per D188 (tZ1Cm).", Context{}), "run `pr-loop reply` again")
	for _, want := range []string{`the reply names "D188" (a plan's ID) and "tZ1Cm" (a review thread's short key)`,
		"never seen", "URL", "short SHA", "key"} {
		if !strings.Contains(got, want) {
			t.Errorf("Refusal = %q, lacking %q", got, want)
		}
	}
	if !refusal.SaysInstead(got) {
		t.Errorf("Refusal = %q, which says no alone", got)
	}
	if Refusal("x", nil, "y") != "" {
		t.Error("a refusal for nothing")
	}
}

// The harness's own repositories are the ones the config lists, matched by
// the GitHub repositories a checkout's remotes fetch from; what a
// repository's files say never makes it one.
func TestIn(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, []byte(`{"publicText": {"harnessRepos": ["o/Harness"]}}`), 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if _, err := git.Run(append([]string{"-C", dir}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("Run `pr-loop` here.\n"), 0o644)
	run("add", "README.md")
	run("-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-qm", "Start")
	if In(dir).Harness {
		t.Error("a repository without remotes that names pr-loop is the harness's")
	}
	run("remote", "add", "origin", "https://github.com/o/product.git")
	if In(dir).Harness {
		t.Error("a product repository that names pr-loop is the harness's")
	}
	run("remote", "add", "upstream", "git@github.com:o/harness.git")
	if !In(dir).Harness {
		t.Error("a checkout of a listed repository is not the harness's")
	}
	t.Setenv("AGENT_TOOLS_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	if In(dir).Harness {
		t.Error("without a config, a repository is the harness's")
	}
	if In(t.TempDir()).Harness {
		t.Error("a directory outside any repository is the harness's")
	}
}
