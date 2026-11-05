package main

import (
	"fmt"
	"os"
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
	// The real launcher runs this binary itself as the child and its probe
	// (TestGitStageNative), in a sandbox that refuses the listener Isolate
	// starts, so they run before it, as pr-loop runs them.
	if len(os.Args) > 1 && (os.Args[1] == "git-stage-child" || os.Args[1] == "git-stage-probe") {
		os.Exit(run(os.Args[1], os.Args[2:], strings.NewReader(""), os.Stdout, os.Stderr))
	}
	gitenv.Isolate()
	hostpaths.BakeFromEnv()
	// Run as codex sandbox, it stands in for Codex's sandbox, which cannot
	// nest in the one tests may run in: it runs the command after --
	// unconfined. It answers the probe as a sandbox that holds would, unless
	// AGENT_GIT_TEST_SANDBOX is leaky, when it runs the probe unconfined too.
	if mode := os.Getenv("AGENT_GIT_TEST_SANDBOX"); (mode == "1" || mode == "leaky") && len(os.Args) > 1 && os.Args[1] == "sandbox" {
		i := slices.Index(os.Args, "--")
		if i < 0 || i+2 >= len(os.Args) {
			os.Exit(2)
		}
		switch command := os.Args[i+2]; {
		case command == "git-stage-probe" && mode == "1":
			fmt.Println("confined")
			os.Exit(0)
		case command == "git-stage-probe" || command == "git-stage-child":
			os.Exit(run(command, os.Args[i+3:], strings.NewReader(""), os.Stdout, os.Stderr))
		}
		os.Exit(2)
	}
	if api := os.Getenv(serveMCPEnv); api != "" {
		gh.SetAPI(api)
		os.Exit(mcpMain(nil, os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}
