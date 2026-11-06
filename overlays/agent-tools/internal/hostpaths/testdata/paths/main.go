// Command paths prints what hostpaths gives a program, one to a line.
package main

import (
	"fmt"

	"github.com/wesnel/agent-tools/internal/env"
	"github.com/wesnel/agent-tools/internal/hostpaths"
)

func main() {
	e := env.Process()
	fmt.Println(hostpaths.ConfigHome(e))
	fmt.Println(hostpaths.StateHome(e))
	fmt.Println(hostpaths.ConfigFile(e, "AGENT_TOOLS_CONFIG", "agent-tools/config.json"))
}
