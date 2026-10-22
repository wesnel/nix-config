// Package agentplans names what the "Agent plans" dashboard session is
// given: the page it works on, from the operator's config, and the
// directories it works in, from the host's XDG state directory.
package agentplans

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// Dir is the dashboard's state directory, $XDG_STATE_HOME/agent-plans.
func Dir() string { return filepath.Join(hostpaths.StateHome(), "agent-plans") }

// Out is where plan-dash export writes the page's documents and the session
// reads the page's database into.
func Out() string { return filepath.Join(Dir(), "out") }

// Session is the session's working directory, which holds its settings.
func Session() string { return filepath.Join(Dir(), "session") }

// ConfigFile is the operator's config of the dashboard,
// agent-tools/agent-plans.json under the XDG config directory: the page's
// URL, kept out of the repository that builds the host.
func ConfigFile() string {
	return hostpaths.ConfigFile("AGENT_PLANS_CONFIG", filepath.Join("agent-tools", "agent-plans.json"))
}

// pageURL is an artifact's claude.ai URL, and nothing after its ID.
var pageURL = regexp.MustCompile(`^https://claude\.ai/(?:code/)?artifact/[A-Za-z0-9-]{1,100}$`)

// URL is the page's URL, from ConfigFile's "url".
func URL() (string, error) {
	data, err := os.ReadFile(ConfigFile())
	if err != nil {
		return "", fmt.Errorf("the Agent plans page's URL: %w", err)
	}
	var config struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("%s: %w", ConfigFile(), err)
	}
	if !pageURL.MatchString(config.URL) {
		return "", fmt.Errorf("%s: url is not an artifact's claude.ai URL, https://claude.ai/artifact/ID", ConfigFile())
	}
	return config.URL, nil
}
