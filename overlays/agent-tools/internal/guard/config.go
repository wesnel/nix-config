package guard

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/wesnel/agent-tools/internal/repos"
)

// settings are the guard's own, from "guard" in the user's
// agent-tools/config.json.
type settings struct {
	// WebSockets are the URL prefixes a Monitor may open a WebSocket to.
	WebSockets []string `json:"webSockets"`
	// CredentialPaths are the absolute path prefixes of credentials, which
	// Claude Code's Read rules are made from too.
	CredentialPaths []string `json:"credentialPaths"`
}

// builtCredentialPaths are wgn.home.llm.guard.credentialPaths as the
// package was built with them: overlays/agent-tools/default.nix generates
// the file that sets them. A path starting ~/ is under the home directory
// of wherever the guard runs, such as a sandbox guest's.
var builtCredentialPaths []string

// credentialPaths are the path prefixes of credentials: the config's, or
// without them the built-in ones, which stand in where the config is
// absent, as in an ECA review-loop session's guest or on a host before its
// first switch. With neither, everything under the home directory is one.
func credentialPaths() []string {
	paths := loadSettings().CredentialPaths
	if len(paths) == 0 {
		paths = builtCredentialPaths
	}
	if len(paths) == 0 {
		return []string{home + "/"}
	}
	expanded := make([]string, len(paths))
	for i, p := range paths {
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			p = home + "/" + rest
		}
		expanded[i] = p
	}
	return expanded
}

// loadSettings reads the guard's settings; a config that is missing or
// unreadable gives none.
func loadSettings() settings {
	var file struct {
		Guard settings `json:"guard"`
	}
	if data, err := os.ReadFile(repos.Path()); err == nil {
		json.Unmarshal(data, &file)
	}
	return file.Guard
}

const webSocket = "A Monitor's WebSocket connects from outside the sandbox to a server the guard cannot check. " +
	"Watch it with a `command` instead, such as a polling loop with `curl`, or ask Wesley to add the URL to the guard's webSockets."

// checkWebSocket denies a WebSocket to a URL no prefix in the guard's
// settings starts.
func checkWebSocket(url string) string {
	for _, prefix := range loadSettings().WebSockets {
		if prefix != "" && strings.HasPrefix(url, prefix) {
			return ""
		}
	}
	return webSocket
}
