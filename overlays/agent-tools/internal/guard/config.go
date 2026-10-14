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
