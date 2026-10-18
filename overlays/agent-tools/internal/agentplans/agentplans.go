// Package agentplans names what the "Agent plans" dashboard session is
// given: the page it works on, from the operator's config, and the
// directories it works in. The guard fences the session by them, so it
// depends on nothing but where the host keeps its config and state.
package agentplans

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// SessionEnv marks the dashboard session. Its systemd unit alone sets it.
const SessionEnv = "AGENT_PLANS_SESSION"

// Schema is the version every document on the page carries as "schema",
// the host's and the page's alike. Fields are only ever added, each
// addition raising it, and never renamed or given another meaning: a
// reader takes any version and uses a field only where it is present.
const Schema = 1

// Dir is the dashboard's state directory, $XDG_STATE_HOME/agent-plans.
func Dir() string { return filepath.Join(hostpaths.StateHome(), "agent-plans") }

// Out is where plan-dash export writes the page's documents and the session
// reads the page's database into: with PageFile, the only files the
// session's tools may name.
func Out() string { return filepath.Join(Dir(), "out") }

// Page is the "Agent plans" page: one HTML file, its CSS and script
// inline, which reads the documents plan-dash export writes from the
// page's database and writes only answers and dispatch documents there.
// Its script renders every document's text as text, never as markup, and
// its schema block names each field it reads; export's page test holds
// both to the documents' types. A diagram's source is also given, where it
// is in the grammar the page's grammar block gives, to Mermaid, loaded by
// its hash in a sandboxed frame with no access to the page, whose SVG the
// page shows only as an image; where it is not drawn, it is shown as text.
//
//go:embed page.html
var Page []byte

// PageHash is the hex SHA-256 of a page's bytes.
func PageHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PageFile is the page's HTML as plan-dash page writes it, the one file the
// session publishes, and only while it holds Page.
func PageFile() string { return filepath.Join(Dir(), "page", "agent-plans.html") }

// PublishedFile holds PageHash of the page last published.
func PublishedFile() string { return filepath.Join(Dir(), "page", "published") }

// PublishingFile holds PageHash of the page the guard last let the session
// publish; plan-dash page published records only that page.
func PublishingFile() string { return filepath.Join(Dir(), "page", "publishing") }

// CreatedFile exists once the guard has let the session create a page,
// which it then lets no session do again: a page it created whose URL the
// operator has not yet configured must not gain siblings.
func CreatedFile() string { return filepath.Join(Dir(), "page", "created") }

// Capabilities are the page's runtime capabilities, which every publish of
// it declares: its database, readable and writable by its owner alone, and
// comments, through which the page wakes the session. A rule here changes
// who can read the plans and write decisions agents act on.
const Capabilities = `{"db":{"rules":[{"path":"","read":"owner","write":"owner"}]},"comments":{}}`

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
