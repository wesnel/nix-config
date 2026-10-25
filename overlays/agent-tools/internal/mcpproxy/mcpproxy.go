// Package mcpproxy reads where this host's mcp-proxy serves its MCP
// servers, from "mcpProxy" in agent-tools' config.json, which the llm
// module writes from the mcp-proxy module's endpoints.
package mcpproxy

import (
	"encoding/json"
	"os"

	"github.com/wesnel/agent-tools/internal/repos"
)

// Config is the host's mcp-proxy.
type Config struct {
	// URL is the proxy's base URL, such as http://127.0.0.1:6290.
	URL string `json:"url"`
	// Endpoints are the paths of the servers the host enables, each by its
	// name, which is its service's key in the guard's mcp.json.
	Endpoints map[string]string `json:"endpoints"`
}

// Load returns the host's mcp-proxy, with no endpoints where the config
// names none, as on a host without the proxy, or cannot be read.
func Load() Config {
	var file struct {
		MCPProxy Config `json:"mcpProxy"`
	}
	if data, err := os.ReadFile(repos.Path()); err == nil {
		json.Unmarshal(data, &file)
	}
	return file.MCPProxy
}

// Serves reports whether the proxy serves the server name.
func (c Config) Serves(name string) bool {
	_, ok := c.Endpoints[name]
	return ok
}
