// Command lima-mcp is an MCP server, on stdio, for Lima: the instances of
// the default home, which it lists and stops with Homebrew's Lima.
package main

import (
	"context"
	"os"
	"time"
)

func main() {
	s := &server{limactl: "/opt/homebrew/bin/limactl", timeout: 5 * time.Minute}
	if err := serve(context.Background(), os.Stdin, os.Stdout, s); err != nil {
		os.Exit(1)
	}
}
