package mcpproxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// standIn starts a stand-in for mcp-proxy, which answers an unauthenticated
// request to a mounted server's route 401 and to any other route 404, as
// the real one does: an authorized server answers 401 and one never
// authorized 404. Each route in routes answers its status;
// every other answers 404, or all when every is set. It returns the
// proxy's URL and how many requests it has had.
func standIn(t *testing.T, routes map[string]int, every int) (string, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch status, ok := routes[r.URL.Path]; {
		case every != 0:
			w.WriteHeader(every)
		case ok:
			w.WriteHeader(status)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL, &requests
}

// refusing starts a listener that closes every connection it accepts, as a
// proxy that is not running leaves its clients with no answer.
func refusing(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	return "http://" + ln.Addr().String()
}

func TestProbe(t *testing.T) {
	endpoints := map[string]string{"linear": "/linear/mcp", "notion": "/notion/mcp"}
	t.Run("a server not connected", func(t *testing.T) {
		url, _ := standIn(t, map[string]int{"/linear/mcp": 401}, 0)
		got := Probe(t.Context(), http.DefaultClient, Config{URL: url, Endpoints: endpoints})
		want := []Finding{{Kind: NotConnected, Server: "notion", URL: url + "/notion/mcp", Status: 404}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
	t.Run("every server connected", func(t *testing.T) {
		url, _ := standIn(t, map[string]int{"/linear/mcp": 401, "/notion/mcp": 401}, 0)
		if got := Probe(t.Context(), http.DefaultClient, Config{URL: url, Endpoints: endpoints}); len(got) != 0 {
			t.Errorf("got %+v, want nothing", got)
		}
	})
	t.Run("a route answering neither 401 nor 404", func(t *testing.T) {
		url, _ := standIn(t, map[string]int{"/linear/mcp": 401, "/notion/mcp": 502}, 0)
		got := Probe(t.Context(), http.DefaultClient, Config{URL: url, Endpoints: endpoints})
		want := []Finding{{Kind: Unexpected, Server: "notion", URL: url + "/notion/mcp", Status: 502}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
	// A proxy that checks its token before routing answers every route
	// 401, connected or not; the probe says it cannot tell rather than
	// reporting every server connected.
	t.Run("every route answers 401", func(t *testing.T) {
		url, _ := standIn(t, nil, 401)
		got := Probe(t.Context(), http.DefaultClient, Config{URL: url, Endpoints: endpoints})
		if len(got) != 1 || got[0].Kind != Undecided || got[0].Server != "" || got[0].Status != 401 {
			t.Errorf("got %+v, want one Undecided finding for the proxy, status 401", got)
		}
	})
	t.Run("proxy unreachable", func(t *testing.T) {
		url := refusing(t)
		got := Probe(t.Context(), http.DefaultClient, Config{URL: url, Endpoints: endpoints})
		if len(got) != 1 || got[0].Kind != Unreachable || got[0].Server != "" || got[0].Err == "" {
			t.Errorf("got %+v, want one Unreachable finding for the proxy, with its error", got)
		}
	})
	t.Run("no proxy on this host", func(t *testing.T) {
		url, requests := standIn(t, nil, 0)
		if got := Probe(t.Context(), http.DefaultClient, Config{URL: url}); len(got) != 0 || requests.Load() != 0 {
			t.Errorf("got %+v after %d requests, want nothing and no request", got, requests.Load())
		}
	})
}

// hanging starts a stand-in for a proxy whose control route answers 404 as
// usual but whose server routes never answer, as a proxy stuck behind a
// dead upstream does. It returns the proxy's URL and the server routes it
// was asked, in order. A real clock is the point: the probe's timeouts are
// what the test exercises.
func hanging(t *testing.T) (string, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/"+control+"/") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) }) // runs before server.Close, which waits for handlers
	return server.URL, &asked
}

// probeWithin runs Probe and fails the test if it has not returned within
// limit, instead of hanging the whole run.
func probeWithin(t *testing.T, limit time.Duration, ctx context.Context, client *http.Client, c Config) []Finding {
	t.Helper()
	done := make(chan []Finding, 1)
	go func() { done <- Probe(ctx, client, c) }()
	select {
	case got := <-done:
		return got
	case <-time.After(limit):
		t.Fatalf("Probe still running after %s", limit)
		return nil
	}
}

func TestProbeTimeouts(t *testing.T) {
	endpoints := map[string]string{"a": "/a/mcp", "b": "/b/mcp", "c": "/c/mcp"}
	// A request to a server's route that fails is a finding, never a
	// connected server.
	t.Run("a server route that does not answer", func(t *testing.T) {
		url, _ := hanging(t)
		got := probeWithin(t, 5*time.Second, t.Context(), &http.Client{Timeout: 100 * time.Millisecond}, Config{URL: url, Endpoints: endpoints})
		if len(got) == 0 {
			t.Fatal("got no findings, want one for a proxy that does not answer")
		}
	})
	// The proxy answers its control route and hangs on the server routes: it
	// is named once, and the routes after the first timeout are not tried, so
	// a session starts at most one request timeout late.
	t.Run("a proxy hanging on server routes is named once", func(t *testing.T) {
		url, asked := hanging(t)
		got := probeWithin(t, 5*time.Second, t.Context(), &http.Client{Timeout: 100 * time.Millisecond}, Config{URL: url, Endpoints: endpoints})
		if len(got) != 1 || got[0].Kind != Unreachable || got[0].Server != "" || got[0].Err == "" {
			t.Errorf("got %+v, want one Unreachable finding for the proxy, with its error", got)
		}
		if !reflect.DeepEqual(*asked, []string{"/a/mcp"}) {
			t.Errorf("asked %v, want only /a/mcp", *asked)
		}
	})
	t.Run("the whole probe is bounded by its context", func(t *testing.T) {
		url, _ := hanging(t)
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		got := probeWithin(t, 5*time.Second, ctx, &http.Client{}, Config{URL: url, Endpoints: endpoints})
		if len(got) != 1 || got[0].Kind != Unreachable {
			t.Errorf("got %+v, want one Unreachable finding", got)
		}
	})
}

// A redirect is not an answer from the proxy: following it would read
// another host's 401 as this server connected.
func TestProbeRedirect(t *testing.T) {
	elsewhere, _ := standIn(t, nil, 401)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/linear/mcp" {
			http.Redirect(w, r, elsewhere+r.URL.Path, http.StatusTemporaryRedirect)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(proxy.Close)
	got := Probe(t.Context(), http.DefaultClient, Config{URL: proxy.URL, Endpoints: map[string]string{"linear": "/linear/mcp"}})
	want := []Finding{{Kind: Unexpected, Server: "linear", URL: proxy.URL + "/linear/mcp", Status: 307}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestFindingString holds each finding's words to naming what to run.
func TestFindingString(t *testing.T) {
	for _, c := range []struct {
		name string
		f    Finding
		says []string
	}{
		{"not connected", Finding{Kind: NotConnected, Server: "notion", URL: "http://p/notion/mcp", Status: 404},
			[]string{"notion", "`mcp-proxy-authorize notion`", "/mcp", "server of your own"}},
		{"unexpected", Finding{Kind: Unexpected, Server: "notion", URL: "http://p/notion/mcp", Status: 502},
			[]string{"notion", "502", "`mcp-proxy-status`"}},
		{"undecided", Finding{Kind: Undecided, URL: "http://p/agent-tools-probe/mcp", Status: 401},
			[]string{"401", "cannot tell", "`mcp-proxy-status`"}},
		{"unreachable", Finding{Kind: Unreachable, URL: "http://p/agent-tools-probe/mcp", Err: "EOF"},
			[]string{"http://p", "EOF", "`mcp-proxy-status`"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := c.f.String()
			for _, want := range c.says {
				if !strings.Contains(s, want) {
					t.Errorf("%q does not say %q", s, want)
				}
			}
		})
	}
}
