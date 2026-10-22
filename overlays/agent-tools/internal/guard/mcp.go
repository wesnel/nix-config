package guard

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// mcpJSON lists, per service, the MCP servers the guard has reviewed, the
// tools of theirs it lets through and the writes the operator confirms.
// mcp-proxy's tool filters are built from the same file, so a tool added
// here is served and allowed together. A server it does not list is
// unreviewed: every tool of its asks in a main session of Claude Code, and
// is refused in Codex, which cannot ask, in review loops and to reviewers.
//
//go:embed mcp.json
var mcpJSON []byte

// mcpService is how the guard treats one service's MCP tools. A tool on one
// of its servers that is neither a read nor a confirmed write, including one
// the server adds later, is refused until it is reviewed and listed.
type mcpService struct {
	service         string
	servers         []string
	reads, confirms map[string]bool
	// readsByName stands in for reads where the vendor publishes no tool
	// list: a tool whose name has a reading verb and no writing one.
	readsByName bool
}

// mcpServices are the services in mcp.json, by their key there.
var mcpServices = func() map[string]*mcpService {
	var raw map[string]struct {
		Service     string   `json:"service"`
		Servers     []string `json:"servers"`
		Reads       []string `json:"reads"`
		Confirms    []string `json:"confirms"`
		ReadsByName bool     `json:"readsByName"`
	}
	if err := json.Unmarshal(mcpJSON, &raw); err != nil {
		panic("mcp.json: " + err.Error())
	}
	services := map[string]*mcpService{}
	for id, r := range raw {
		services[id] = &mcpService{service: r.Service, servers: r.Servers, reads: set(r.Reads...),
			confirms: set(r.Confirms...), readsByName: r.ReadsByName}
	}
	return services
}()

// mcpByServer is each listed service by the lower-cased name a harness gives
// one of its servers.
var mcpByServer = func() map[string]*mcpService {
	byServer := map[string]*mcpService{}
	for _, s := range mcpServices {
		for _, name := range s.servers {
			byServer[strings.ToLower(name)] = s
		}
	}
	return byServer
}()

// unlistedServer is why a main session's call to a server mcp.json does not
// list asks; unlistedServerCodex and unlistedServerReviewer why it is
// refused where the operator cannot be asked or a reviewer makes it.
const (
	unlistedServer = "This MCP server is not in the guard's mcp.json, so none of its tools has been reviewed. " +
		"The operator confirms each call; reviewing the server into mcp.json, its reads and the writes that ask, " +
		"stops the asking."
	unlistedServerCodex = "This MCP server is not in the guard's mcp.json, and Codex cannot ask the operator to confirm " +
		"a call to an unreviewed server, so the guard refuses it. Use it from Claude Code, which asks, or ask the " +
		"operator to review the server into mcp.json."
	unlistedServerReviewer = "This MCP server is not in the guard's mcp.json, and reviewers use only reviewed " +
		"servers. Put what you wanted from it in your report for the operator."
)

// asks are the reasons the guard asks the operator about rather than denies,
// where the harness can ask: a write each service's confirms, and a call to
// an unlisted server.
var asks = func() map[string]bool {
	a := map[string]bool{unlistedServer: true}
	for _, s := range mcpServices {
		a[s.confirmReason()] = true
	}
	return a
}()

// cannotAsk is the reason in asks for Codex, whose hooks cannot ask.
func cannotAsk(reason string) string {
	if reason == unlistedServer {
		return unlistedServerCodex
	}
	return "Codex cannot ask the operator to confirm this write, so the guard refuses it. Make it from Claude Code, which asks, or put the draft in your report for the operator."
}

// mcpTool returns the listed service whose server makes the call and the
// tool's own name, with mcp false for a tool on no MCP server. Claude Code
// and Codex name a tool mcp__SERVER__TOOL.
func mcpTool(tool string) (service *mcpService, name string, mcp bool) {
	rest, ok := strings.CutPrefix(tool, "mcp__")
	if !ok {
		return nil, "", false
	}
	server, name, _ := strings.Cut(rest, "__")
	return mcpByServer[strings.ToLower(server)], name, true
}

// checkMCP returns why who may not call the MCP tool name on a server of
// service, or of none mcp.json lists when service is nil, or "".
func checkMCP(service *mcpService, name string, who role) string {
	switch {
	case service != nil:
		return service.check(name, who)
	case who == baseline:
		return unlistedServer
	}
	return unlistedServerReviewer
}

func (s *mcpService) confirmReason() string {
	return "Writing to " + s.service + " acts as the operator, who confirms each write. If they decline, put the draft in your report."
}

// check returns why who may not call the service's tool name, or "".
func (s *mcpService) check(name string, who role) string {
	confirm := s.confirms[name]
	switch {
	case s.reads[name] || s.readsByName && readName(name):
		return ""
	case confirm && who == reviewer:
		return reviewerReport
	case confirm:
		return s.confirmReason()
	}
	return "The guard lets through only the " + s.service + " tools it has reviewed, and " + name +
		" is not a read among them. Use a read, or put what you wanted it to do in your report for the operator."
}

// mcpReads and ngrokWrites are the verbs that make a tool's name a read or
// a write, as whole words between `_` or `-`.
var mcpReads = set("get", "list", "search", "read", "query", "describe", "show", "find", "fetch", "view", "lookup",
	"retrieve")

// readName reports whether a tool's name has a reading verb and no writing
// one.
func readName(name string) bool {
	read := false
	for _, w := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '_' || r == '-' }) {
		if ngrokWrites[w] {
			return false
		}
		read = read || mcpReads[w]
	}
	return read
}
