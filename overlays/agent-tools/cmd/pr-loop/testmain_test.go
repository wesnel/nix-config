package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// TestMain runs the tests; or, started by a TestMCP case with serveMCPEnv
// set to its fake GitHub's URL, serves as pr-loop mcp against it; or, as
// git_stage's launcher or its child, runs the child.
func TestMain(m *testing.M) {
	gitenv.Isolate()
	hostpaths.BakeFromEnv()
	// Named codex, it stands in for Codex's sandbox, which cannot nest in
	// the one tests may run in: it runs the command after -- unconfined.
	if os.Getenv("AGENT_GIT_TEST_SANDBOX") == "1" && filepath.Base(os.Args[0]) == "codex" {
		i := slices.Index(os.Args, "--")
		if i < 0 || i+2 >= len(os.Args) || os.Args[i+2] != "git-stage-child" {
			os.Exit(2)
		}
		os.Exit(run("git-stage-child", os.Args[i+3:], strings.NewReader(""), os.Stdout, os.Stderr))
	}
	// The real launcher runs this binary itself as the child
	// (TestGitStageNative).
	if len(os.Args) > 1 && os.Args[1] == "git-stage-child" {
		os.Exit(run("git-stage-child", os.Args[2:], strings.NewReader(""), os.Stdout, os.Stderr))
	}
	if api := os.Getenv(serveMCPEnv); api != "" {
		gh.SetAPI(api)
		os.Exit(mcpMain(nil, os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}
