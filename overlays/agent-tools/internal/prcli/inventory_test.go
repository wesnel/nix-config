package prcli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gh"
)

func TestInventoryCommand(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PR_LOOP_MODE", "")
	t.Setenv("GH_TOKEN", "test-token")
	// GitHub is represented by a local server; this command must not need a checkout.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/pulls" || r.URL.Query().Get("state") != "all" {
			t.Errorf("unexpected inventory request: %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(server.Close)
	restore := gh.SetAPI(server.URL)
	t.Cleanup(restore)
	var out, errOut bytes.Buffer
	code := Main([]string{"list", "--repo", "owner/repo"}, strings.NewReader(""), &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), `"pullRequests": []`) || !strings.Contains(out.String(), `"repository": "owner/repo"`) {
		t.Fatalf("exit %d; stdout %s; stderr %s", code, &out, &errOut)
	}
}
