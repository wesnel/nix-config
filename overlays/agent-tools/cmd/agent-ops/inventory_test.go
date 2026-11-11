package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPRInventoryTool(t *testing.T) {
	f := newMCPFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string
			Variables struct{ Owner, Name string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || r.Method != http.MethodPost || r.URL.Path != "/graphql" || r.Header.Get("Authorization") != "Bearer "+fakeToken || request.Variables.Owner != "o" || request.Variables.Name != "r" || !strings.Contains(request.Query, "states: [OPEN]") {
			t.Errorf("unexpected inventory request: %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"repository":{"nameWithOwner":"o/r","pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`)
	}))
	t.Cleanup(server.Close)
	for i, entry := range f.env {
		if strings.HasPrefix(entry, serveMCPEnv+"=") {
			f.env[i] = serveMCPEnv + "=" + server.URL
		}
	}
	c := f.start(t)
	r := c.tool(t, "agent_pr_list", map[string]any{"repo": "o/r"})
	if r.isError || !strings.Contains(r.out, `"pullRequests": []`) || !strings.Contains(r.out, `"repository": "o/r"`) {
		t.Fatalf("inventory without worktree: %+v", r)
	}
}
