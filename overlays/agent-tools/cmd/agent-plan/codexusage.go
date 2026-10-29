package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/wesnel/agent-tools/internal/codexrate"
	quota "github.com/wesnel/agent-tools/internal/usage"
)

// codexUsage records the Codex account's quota windows in quota.CodexFile,
// read through the codex on PATH, for `quota-stop --harness codex`. It
// runs from a timer outside every session, since Codex needs its login
// and the network, which a session's sandbox denies.
func codexUsage(args []string, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "agent-plan codex-usage: takes no arguments")
		return 2
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		fmt.Fprintln(stderr, "agent-plan codex-usage:", err)
		return 1
	}
	result, err := codexrate.Read(context.Background(), codex)
	if err != nil {
		fmt.Fprintln(stderr, "agent-plan codex-usage:", err)
		return 1
	}
	ws, ok := quota.FromCodexRateLimits(result, time.Now())
	if !ok {
		fmt.Fprintln(stderr, "agent-plan codex-usage: Codex reported neither a 5h nor a 7d window")
		return 1
	}
	if err := quota.Record(quota.CodexFile(), ws); err != nil {
		fmt.Fprintln(stderr, "agent-plan codex-usage:", err)
		return 1
	}
	return 0
}
