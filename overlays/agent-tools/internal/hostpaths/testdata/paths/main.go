// Command paths prints what hostpaths gives a program, one to a line.
package main

import (
	"fmt"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

func main() {
	fmt.Println(hostpaths.ConfigHome())
	fmt.Println(hostpaths.StateHome())
	fmt.Println(hostpaths.ConfigFile("AGENT_TOOLS_CONFIG", "agent-tools/config.json"))
}
