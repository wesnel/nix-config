package mcpproxy

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// Kind is what a probe found wrong.
type Kind int

const (
	// Unreachable: the proxy did not answer, so none of its servers is
	// available.
	Unreachable Kind = iota + 1
	// Undecided: the proxy answered a server it does not serve with
	// something other than 404, so a 401 from a server's route no longer
	// says it is connected.
	Undecided
	// NotConnected: the server's route answers 404, as the proxy's does for
	// a server it could not connect (never authorized, or its token
	// refused).
	NotConnected
	// Unexpected: the server's route answers neither 401 nor 404.
	Unexpected
)

// Finding is one thing a probe found wrong.
type Finding struct {
	Kind Kind
	// Server is the server it concerns, or "" for the proxy as a whole.
	Server string
	// URL is the route whose answer showed it.
	URL string
	// Status is the HTTP status that showed it, or 0 for a failed request.
	Status int
	// Err is why a request failed.
	Err string
}

// control is a server name no proxy serves: its route must answer 404, or
// a server's 404 says nothing.
const control = "agent-tools-probe"

// Probe returns what is wrong with the proxy and its servers, or nothing
// when each server's route is mounted. It sends no credentials: the proxy
// mounts a route only for a server it connected, and answers a request
// without its token 401 on a mounted route and 404 on any other.
func Probe(ctx context.Context, client *http.Client, c Config) []Finding {
	if len(c.Endpoints) == 0 {
		return nil
	}
	base := strings.TrimSuffix(c.URL, "/")
	url := base + "/" + control + "/mcp"
	switch status, err := post(ctx, client, url); {
	case err != nil:
		return []Finding{{Kind: Unreachable, URL: url, Err: err.Error()}}
	case status != http.StatusNotFound:
		return []Finding{{Kind: Undecided, URL: url, Status: status}}
	}
	var findings []Finding
	for _, name := range slices.Sorted(maps.Keys(c.Endpoints)) {
		url := base + c.Endpoints[name]
		switch status, err := post(ctx, client, url); {
		case err != nil:
			// The proxy answered the control route and then failed this
			// one, most likely by hanging. Name it once rather than once
			// per server, and try no more routes: each would wait out the
			// same timeout.
			return []Finding{{Kind: Unreachable, URL: base + "/" + control + "/mcp", Err: err.Error()}}
		case status == http.StatusNotFound:
			findings = append(findings, Finding{Kind: NotConnected, Server: name, URL: url, Status: status})
		case status != http.StatusUnauthorized:
			findings = append(findings, Finding{Kind: Unexpected, Server: name, URL: url, Status: status})
		}
	}
	return findings
}

// post sends an empty POST to url and returns the status it answers, which
// is a redirect's own when the proxy answers one.
func post(ctx context.Context, client *http.Client, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return 0, err
	}
	// A redirect is not the proxy's answer: following it to another host
	// would read that host's 401 as this server connected.
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// String says what the finding means and what to do, in words for an agent
// or the operator.
func (f Finding) String() string {
	answer := fmt.Sprint(f.Status)
	if f.Err != "" {
		answer = "no answer (" + f.Err + ")"
	}
	switch f.Kind {
	case Unreachable:
		return fmt.Sprintf("mcp-proxy at %s gave %s, so none of its MCP servers is in this session. "+
			"Ask the operator to run `mcp-proxy-status` and look at the proxy; do not add a server of your own in its place.",
			strings.TrimSuffix(f.URL, "/"+control+"/mcp"), answer)
	case Undecided:
		return fmt.Sprintf("mcp-proxy answered %s for %s, a server it does not serve, where it answered 404, so this "+
			"check cannot tell which of its servers are connected. Ask the operator to run `mcp-proxy-status`.", answer, f.URL)
	case NotConnected:
		return fmt.Sprintf("mcp-proxy's %s server is not connected (%s answered 404), so its tools are missing from "+
			"this session: it was never authorized, or its token was refused. Ask the operator to run "+
			"`mcp-proxy-authorize %s`, then reconnect it with /mcp in Claude Code or start a new session; do not add a "+
			"server of your own in its place.", f.Server, f.URL, f.Server)
	}
	return fmt.Sprintf("mcp-proxy's %s server gave %s at %s, neither connected (401) nor missing (404). "+
		"Ask the operator to run `mcp-proxy-status`.", f.Server, answer, f.URL)
}
