package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/wesnel/agent-tools/internal/codexrate"
	"github.com/wesnel/agent-tools/internal/env"
	quota "github.com/wesnel/agent-tools/internal/usage"
)

// codexUsage records the Codex account's quota windows in quota.CodexFile,
// read through the codex on PATH, for `quota-stop --harness codex`. It
// runs from a timer outside every session, since Codex needs its login
// and the network, which a session's sandbox denies.
func codexUsage(ctx context.Context, args []string, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "agent-plan codex-usage: takes no arguments")
		return 2
	}
	// codexrate starts codex in the process environment, so codex is
	// found on the process's PATH too.
	codex, err := exec.LookPath("codex")
	if err != nil {
		fmt.Fprintln(stderr, "agent-plan codex-usage:", err)
		return 1
	}
	result, err := codexrate.Read(ctx, codex)
	if err != nil {
		fmt.Fprintln(stderr, "agent-plan codex-usage:", err)
		return 1
	}
	ws, ok := quota.FromCodexRateLimits(result, time.Now())
	if !ok {
		fmt.Fprintln(stderr, "agent-plan codex-usage: Codex reported neither a 5h nor a 7d window")
		return 1
	}
	if err := quota.Record(quota.CodexFile(env.From(ctx)), ws); err != nil {
		fmt.Fprintln(stderr, "agent-plan codex-usage:", err)
		return 1
	}
	return 0
}
