// Command lima-mcp is an MCP server, on stdio, for Lima: the instances of
// the default home, which it lists and stops with Homebrew's Lima.
//
// lima-mcp qemu-guard --config FILE -- ARGS is what Lima runs as QEMU for
// the managed VM: it checks ARGS and executes QEMU confined, or refuses.
// lima-mcp lima-egress connect|quic --config FILE relays one guest
// connection, as the managed VM's QEMU runs it for each.
package main

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "qemu-guard" {
		// Lima logs QEMU's stderr in the instance directory.
		err := guardMain(os.Args[2:], syscall.Exec, "/nix/store/")
		fmt.Fprintln(os.Stderr, "lima-mcp qemu-guard:", err)
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
	s := &server{limactl: "/opt/homebrew/bin/limactl", timeout: 5 * time.Minute}
	if err := serve(context.Background(), os.Stdin, os.Stdout, s); err != nil {
		os.Exit(1)
	}
}
