package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/env"
)

// The token is the Env's own: $GH_TOKEN, else $GITHUB_TOKEN. Neither case
// reaches `gh auth token`, which runs once in a process.
func TestToken(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		vars []string
		want string
	}{
		{"GH_TOKEN", []string{"GH_TOKEN=from-gh", "GITHUB_TOKEN=from-github"}, "from-gh"},
		{"GITHUB_TOKEN", []string{"GITHUB_TOKEN=from-github"}, "from-github"},
		{"an empty GH_TOKEN", []string{"GH_TOKEN=", "GITHUB_TOKEN=from-github"}, "from-github"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ctx := env.With(t.Context(), env.New(t.TempDir(), c.vars...))
			if got, err := Token(ctx); err != nil || got != c.want {
				t.Errorf("Token = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

// Every call made with a context from WithAPI goes to that context's
// server, with the Env's token, and two contexts with different servers,
// in use at once, each reach their own. An error names the path without
// the server.
func TestWithAPI(t *testing.T) {
	t.Parallel()
	serve := func(t *testing.T, name string) context.Context {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Header.Get("Authorization") != "Bearer test-token":
				http.Error(w, "no token", http.StatusUnauthorized)
			case r.URL.Path == "/graphql":
				fmt.Fprintf(w, `{"data": {"server": %q}}`, name)
			case r.URL.Path == "/missing":
				http.NotFound(w, r)
			default:
				fmt.Fprintf(w, `{"server": %q, "path": %q}`, name, r.URL.Path)
			}
		}))
		t.Cleanup(srv.Close)
		return WithAPI(env.With(t.Context(), env.New(t.TempDir(), "GH_TOKEN=test-token")), srv.URL+"/")
	}
	for _, name := range []string{"one", "two"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := serve(t, name)
			want := `{"server": "` + name + `", "path": "/repos/o/r"}`
			var rest map[string]string
			if err := REST(ctx, "GET", "repos/o/r", nil, &rest); err != nil || rest["server"] != name || rest["path"] != "/repos/o/r" {
				t.Errorf("REST: %v, %v; want server %s", rest, err, name)
			}
			if raw, err := Raw(ctx, "/repos/o/r"); err != nil || string(raw) != want {
				t.Errorf("Raw: %s, %v; want %s", raw, err, want)
			}
			if read, err := Read(ctx, "repos/o/r", "application/json"); err != nil || string(read) != want {
				t.Errorf("Read: %s, %v; want %s", read, err, want)
			}
			var q map[string]string
			if err := GraphQL(ctx, "{ viewer { login } }", nil, &q); err != nil || q["server"] != name {
				t.Errorf("GraphQL: %v, %v; want server %s", q, err, name)
			}
			var status *StatusError
			if err := REST(ctx, "GET", "missing", nil, nil); !errors.As(err, &status) || status.Code != 404 ||
				!strings.HasPrefix(err.Error(), "GET /missing: 404") {
				t.Errorf("a missing path: %v; want a 404 naming GET /missing", err)
			}
		})
	}
}

// fakeGh is a gh that prints its argument list and one variable of its
// environment.
const fakeGh = "#!/bin/sh\necho \"$* $FAKE_GH_SAYS\"\n"

// gh runs the gh of the Env's $PATH, with the Env's variables, outside
// any repository; one the Env's $PATH lacks is not found, whatever this
// process's $PATH holds.
func TestCommand(t *testing.T) {
	t.Parallel()
	t.Run("the Env's gh and variables", func(t *testing.T) {
		t.Parallel()
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGh), 0o755); err != nil {
			t.Fatal(err)
		}
		ctx := env.With(t.Context(), env.New(t.TempDir(), "PATH="+bin+":/usr/bin:/bin", "FAKE_GH_SAYS=hello"))
		cmd := Command(ctx, "api", "graphql")
		out, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(out)) != "api graphql hello" {
			t.Errorf("ran %s: %q, %v; want the fake's \"api graphql hello\"", cmd.Path, out, err)
		}
		if cmd.Dir != Outside {
			t.Errorf("ran in %s, want %s", cmd.Dir, Outside)
		}
	})
	t.Run("none on the Env's PATH", func(t *testing.T) {
		t.Parallel()
		ctx := env.With(t.Context(), env.New(t.TempDir(), "PATH="+t.TempDir()))
		_, err := Command(ctx, "auth", "token").Output()
		if !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("ran a gh the Env's PATH lacks: %v", err)
		}
	})
	t.Run("an Env with no variables", func(t *testing.T) {
		t.Parallel()
		cmd := Command(env.With(context.Background(), env.New(t.TempDir())), "auth", "token")
		if cmd.Env == nil || len(cmd.Env) != 0 {
			t.Errorf("Env %v, want empty and not nil, which would be this process's", cmd.Env)
		}
	})
}
