// Command sandbox-relay runs chosen programs on the host for a sandbox that
// has no credentials. See the usage text.
package main

import (
	"fmt"
	"os"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/relay"
)

const usage = `sandbox-relay: run programs on the host for a sandbox without credentials.

  serve --workspace DIR [--guest-path DIR] [--program NAME...] (--listen HOST:PORT | --socket PATH)
          serve until killed
  session [--workspace DIR] [--program NAME...] -- COMMAND...
          run COMMAND beside a relay on a private socket, and with
          PR_LOOP_SESSION=1 record where it runs for pr-loop stop: in a
          process group of its own, or in this process's where it leads
          one, as a shell's job does, or is a terminal's foreground group;
          COMMAND gets
          ` + relay.URLEnv + `, and "{socket}" and "{workspace}" in its
          arguments become their paths

Commands run only inside the workspace (default for session: the repository
containing the current directory). --program names a program clients may run,
found beside sandbox-relay or on PATH (default: pr-loop). Each program runs
with ` + relay.RelayEnv + `=1 and decides which of its commands to accept.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if cli.IsHelp(os.Args[1]) {
		fmt.Fprint(os.Stdout, usage)
		os.Exit(0)
	}
	switch os.Args[1] {
	case "serve":
		os.Exit(relay.Serve(os.Args[2:], os.Stderr))
	case "session":
		os.Exit(relay.Session(os.Args[2:], os.Stderr))
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}
