// Command lima-mcp is an MCP server, on stdio, for Lima: the instances of
// the default home, which it lists and stops with Homebrew's Lima, and,
// with --config FILE, the managed VM that FILE configures,
// with the pinned Lima in a home of its own.
//
// lima-mcp qemu-guard --config FILE -- ARGS is what Lima runs as QEMU for
// the managed VM: it checks ARGS and executes QEMU confined, or refuses.
// lima-mcp lima-dev --config FILE -- ARGS is the operator's limactl for the
// managed home.
// lima-mcp lima-egress connect|quic --config FILE relays one guest
// connection, as the managed VM's QEMU runs it for each.
// lima-mcp export --outbox DIR NAME, run by a session in its repository,
// writes the bundle lima_dev_import NAME carries into the managed VM.
package main

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/limavm"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "qemu-guard" {
		// Lima logs QEMU's stderr in the instance directory.
		err := guardMain(os.Args[2:], syscall.Exec, "/nix/store/")
		fmt.Fprintln(os.Stderr, "lima-mcp qemu-guard:", err)
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "lima-dev" {
		err := devMain(os.Args[2:], syscall.Exec)
		fmt.Fprintln(os.Stderr, "lima-dev:", err)
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "lima-egress" {
		// Its stderr is the guest's connection: an error is logged, if
		// anywhere, in the egress log.
		if egressMain(os.Args[2:], os.Stdin) != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "export" {
		if err := exportMain(context.Background(), os.Args[2:], "."); err != nil {
			fmt.Fprintln(os.Stderr, "lima-mcp export:", err)
			os.Exit(1)
		}
		return
	}
	s := &server{limactl: "/opt/homebrew/bin/limactl", timeout: 5 * time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if len(os.Args) == 3 && os.Args[1] == "--config" {
		c, err := limavm.LoadConfig(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "lima-mcp:", err)
			os.Exit(2)
		}
		s.managed = newManaged(c, os.Args[2], limavm.UserProcesses, limavm.ProcessArgs)
		defer s.managed.close()
		go s.managed.watch(ctx, watchInterval, 0)
	} else if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: lima-mcp [--config FILE]")
		os.Exit(2)
	}
	if err := serve(ctx, os.Stdin, os.Stdout, s); err != nil {
		os.Exit(1)
	}
}
