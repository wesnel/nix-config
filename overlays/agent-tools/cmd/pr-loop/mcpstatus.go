package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/mcpproxy"
)

// The probe's limits: a SessionStart hook waits for it, so a proxy that does
// not answer delays a session by at most probeTimeout, and each request by
// at most requestTimeout. Variables so a test can shorten them.
var (
	probeTimeout   = 5 * time.Second
	requestTimeout = 2 * time.Second
)

// mcpStatus probes this host's mcp-proxy and prints what is wrong with it,
// one finding a line, or with --hook as Claude Code's SessionStart hook
// output, for the agent and the operator alike; it prints nothing when every server is connected. A
// failure to probe is itself a finding, so it always exits 0 once its
// arguments are right: a SessionStart hook's failure would only hide it.
func mcpStatus(args []string, stdout, stderr io.Writer) int {
	hook := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--hook":
		hook = true
	default:
		fmt.Fprintln(stderr, "pr-loop: mcp-status takes only --hook; see pr-loop --help")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	findings := mcpproxy.Probe(ctx, &http.Client{Timeout: requestTimeout}, mcpproxy.Load())
	if len(findings) == 0 {
		return 0
	}
	lines := make([]string, len(findings))
	for i, f := range findings {
		lines[i] = f.String()
	}
	if !hook {
		fmt.Fprintln(stdout, strings.Join(lines, "\n"))
		return 0
	}
	// additionalContext reaches only the agent; systemMessage is shown to
	// the operator, who is the one to run the command a finding names.
	text := strings.Join(lines, "\n")
	json.NewEncoder(stdout).Encode(map[string]any{"systemMessage": text, "hookSpecificOutput": map[string]string{
		"hookEventName": "SessionStart", "additionalContext": text}})
	return 0
}
