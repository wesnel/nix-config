// Command agent-pr publishes and manages pull requests.
package main

import (
	"os"

	"github.com/wesnel/agent-tools/internal/prcli"
)

func main() {
	os.Exit(prcli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
