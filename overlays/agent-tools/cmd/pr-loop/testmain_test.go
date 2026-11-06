package main

import (
	"context"
	"os"
	"testing"

	"github.com/wesnel/agent-tools/internal/env"
	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/gitenv"
)

// TestMain runs the tests, or, started by a TestMCP case with serveMCPEnv
// set to its fake GitHub's URL, serves as pr-loop mcp against it.
func TestMain(m *testing.M) {
	gitenv.Isolate()
	if api := os.Getenv(serveMCPEnv); api != "" {
		ctx := gh.WithAPI(env.With(context.Background(), env.Process()), api)
		os.Exit(mcpMain(ctx, nil, os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}
