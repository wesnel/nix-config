// Package agentplans names what the "Agent plans" dashboard session is
// given: the pages it works on, from the operator's config, and the
// directories it works in, from the host's XDG state directory.
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
	"github.com/wesnel/agent-tools/internal/repos"
)

// Schema is the version every document on the page carries as "schema".
// Fields are only ever added, each addition raising it, and never renamed
// or given another meaning: a reader takes any version and uses a field
// only where it is present.
const Schema = 2

// Dir is the dashboard's state directory, $XDG_STATE_HOME/agent-plans.
func Dir() string { return filepath.Join(hostpaths.StateHome(), "agent-plans") }

// Out is where plan-dash export writes the page's documents and the session
// reads the page's database into.
func Out() string { return filepath.Join(Dir(), "out") }

// Page is the "Agent plans" page: one HTML file, its CSS and script
// inline, which reads the documents plan-dash export writes from the
// page's database and writes nothing. Its script renders every document's text as text, never as markup, and
// its schema block names each field it reads; export's page test holds
// both to the documents' types. A review record's diagram source is also
// given, where it is in the grammar the page's grammar block gives, to
// Mermaid, loaded by its hash in a sandboxed frame with no access to the
// page, whose SVG the page shows only as an image; where it is not drawn,
// it is shown as text.
//
//go:embed page.html
var Page []byte

// PageHash is the hex SHA-256 of a page's bytes.
func PageHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PageFile is the page's HTML as plan-dash page writes it, the one file the
// session publishes.
func PageFile() string { return filepath.Join(Dir(), "page", "agent-plans.html") }

// PublishedFile holds PageHash of the page last published.
func PublishedFile() string { return filepath.Join(Dir(), "page", "published") }

// Capabilities are the page's runtime capabilities, which every publish of
// it declares: its database, readable and writable by its owner alone,
// which the session writes as the owner; the page itself writes nothing. A
// rule here changes who can read the plans.
const Capabilities = `{"db":{"rules":[{"path":"","read":"owner","write":"owner"}]}}`

// Session is the session's working directory, which holds its settings.
func Session() string { return filepath.Join(Dir(), "session") }

// Targets are where the dashboard is published, each "" where the machine
// sets none: "agentPlans" in agent-tools' config.json, which the llm
// module writes from wgn.home.llm.agentPlans. The file is the operator's,
// read-only and guarded, so no session can point publication elsewhere.
type Targets struct {
	// ClaudePage is the Agent plans artifact's claude.ai URL.
	ClaudePage string `json:"claudePage"`
	// SpacePage is the ID of the ChatGPT Space page plan-dash space
	// publishes to.
	SpacePage string `json:"spacePage"`
}

var (
	// pageURL is an artifact's claude.ai URL, and nothing after its ID.
	pageURL = regexp.MustCompile(`^https://claude\.ai/(?:code/)?artifact/[A-Za-z0-9-]{1,100}$`)
	// spacePage is a Space page's ID, as read_page takes it.
	spacePage = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
)

// LoadTargets reads Targets, refusing a value that is set but not the
// shape of its kind.
func LoadTargets() (Targets, error) {
	file := repos.Path()
	data, err := os.ReadFile(file)
	if err != nil {
		return Targets{}, fmt.Errorf("the Agent plans targets: %w", err)
	}
	var config struct {
		AgentPlans struct {
			ClaudePage *string `json:"claudePage"`
			SpacePage  *string `json:"spacePage"`
		} `json:"agentPlans"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return Targets{}, fmt.Errorf("%s: %w", file, err)
	}
	var t Targets
	if p := config.AgentPlans.ClaudePage; p != nil {
		if !pageURL.MatchString(*p) {
			return Targets{}, fmt.Errorf("%s: agentPlans.claudePage is not an artifact's claude.ai URL, https://claude.ai/artifact/ID", file)
		}
		t.ClaudePage = *p
	}
	if p := config.AgentPlans.SpacePage; p != nil {
		if !spacePage.MatchString(*p) {
			return Targets{}, fmt.Errorf("%s: agentPlans.spacePage is not a Space page's ID", file)
		}
		t.SpacePage = *p
	}
	return t, nil
}
