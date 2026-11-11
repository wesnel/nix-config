package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPRInventoryTool(t *testing.T) {
	c := githubReadClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/pulls" || r.URL.Query().Get("state") != "open" {
			t.Errorf("unexpected inventory request: %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "[]")
	})
	r := c.tool(t, "agent_pr_list", map[string]any{"repo": "o/r"})
	if r.isError || !strings.Contains(r.out, `"pullRequests": []`) || !strings.Contains(r.out, `"repository": "o/r"`) {
		t.Fatalf("inventory without worktree: %+v", r)
	}
}
