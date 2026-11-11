package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMCPStatus covers mcp-status --hook, Claude Code's SessionStart hook:
// with a proxy server not connected, it adds context naming the command
// that connects it, and shows the operator the same as a systemMessage,
// since additionalContext reaches only the agent; with every server
// connected, or no proxy on the host,
// it prints nothing. The proxy is a stand-in answering as mcp-proxy does:
// 401 on a mounted server's route, 404 on any other.
func TestMCPStatus(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/linear/mcp" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(proxy.Close)
	for _, c := range []struct {
		name, config string
		says         string // "" for no output
	}{
		{"a server not connected", `{"mcpProxy": {"url": "` + proxy.URL + `", "endpoints": {"linear": "/linear/mcp", "notion": "/notion/mcp"}}}`,
			"`mcp-proxy-authorize notion`"},
		{"every server connected", `{"mcpProxy": {"url": "` + proxy.URL + `", "endpoints": {"linear": "/linear/mcp"}}}`, ""},
		{"no proxy on this host", `{"mcpProxy": null}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "config.json")
			os.WriteFile(config, []byte(c.config), 0o600)
			t.Setenv("AGENT_TOOLS_CONFIG", config)
			t.Chdir(t.TempDir())
			var stdout, stderr bytes.Buffer
			code := run("mcp-status", []string{"--hook"}, strings.NewReader(`{"hook_event_name":"SessionStart","source":"startup"}`), &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit %d\n%s", code, stderr.String())
			}
			if c.says == "" {
				if stdout.Len() != 0 {
					t.Errorf("printed %s, want nothing", stdout.String())
				}
				return
			}
			var out struct {
				System string `json:"systemMessage"`
				Hook   struct {
					Event   string `json:"hookEventName"`
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
				t.Fatalf("printed %q: %v", stdout.String(), err)
			}
			if out.Hook.Event != "SessionStart" || !strings.Contains(out.Hook.Context, c.says) ||
				strings.Contains(out.Hook.Context, "mcp-proxy-authorize linear") {
				t.Errorf("printed %s, want SessionStart context naming %s and not linear", stdout.String(), c.says)
			}
			if !strings.Contains(out.System, c.says) || strings.Contains(out.System, "mcp-proxy-authorize linear") {
				t.Errorf("printed %s, want a systemMessage for the operator naming %s and not linear", stdout.String(), c.says)
			}
		})
	}
}

// TestMCPStatusTimeouts holds mcp-status to returning, and naming the proxy,
// when it answers its control route and then never answers a server's:
// a SessionStart hook must not hang the session on it.
func TestMCPStatusTimeouts(t *testing.T) {
	release := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/linear/mcp" {
			http.NotFound(w, r)
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(proxy.Close)
	t.Cleanup(func() { close(release) }) // runs before proxy.Close, which waits for handlers
	for _, c := range []struct {
		name           string
		probe, request time.Duration
	}{
		{"the request timeout ends a hanging route", time.Minute, 100 * time.Millisecond},
		{"the probe timeout ends a hanging route", 100 * time.Millisecond, time.Minute},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Cleanup(func(p, r time.Duration) func() {
				return func() { probeTimeout, requestTimeout = p, r }
			}(probeTimeout, requestTimeout))
			probeTimeout, requestTimeout = c.probe, c.request
			config := filepath.Join(t.TempDir(), "config.json")
			os.WriteFile(config, []byte(`{"mcpProxy": {"url": "`+proxy.URL+`", "endpoints": {"linear": "/linear/mcp"}}}`), 0o600)
			t.Setenv("AGENT_TOOLS_CONFIG", config)
			t.Chdir(t.TempDir())
			var stdout, stderr bytes.Buffer
			done := make(chan int, 1)
			go func() {
				done <- run("mcp-status", nil, strings.NewReader(""), &stdout, &stderr)
			}()
			select {
			case code := <-done:
				if code != 0 || !strings.Contains(stdout.String(), "gave no answer") {
					t.Errorf("exit %d, printed %q, want exit 0 and a proxy that gave no answer", code, stdout.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("mcp-status still running after 10s")
			}
		})
	}
}
