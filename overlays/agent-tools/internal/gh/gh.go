// Package gh talks to the GitHub API with the user's existing `gh` login.
package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wesnel/agent-tools/internal/env"
)

// api is where every call goes, with the user's token: GitHub's API, or a
// test's server, which SetAPI points it at.
var api = "https://api.github.com"

// SetAPI sends every later call to url, a test's own server, and returns
// the function that restores GitHub's.
func SetAPI(url string) (restore func()) {
	old := api
	api = strings.TrimSuffix(url, "/")
	return func() { api = old }
}

// Outside is where pr-loop runs gh: gh calls git with the repository's own
// config and hooks when started inside one, and pr-loop names the repository
// explicitly instead.
var Outside = os.TempDir()

var (
	tokenOnce sync.Once
	token     string
	tokenErr  error
	client    = &http.Client{Timeout: 2 * time.Minute}
)

// Command is gh with args, killed when ctx is done, run in Outside with the
// variables of ctx's Env and the gh its $PATH finds.
func Command(ctx context.Context, args ...string) *exec.Cmd {
	e := env.From(ctx)
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = Outside
	// Never nil, which would hand gh this process's environment.
	cmd.Env = append([]string{}, e.Environ()...)
	cmd.Path, cmd.Err = lookPath(e, "gh")
	return cmd
}

// lookPath is the executable name in a directory of e's $PATH, as
// exec.LookPath finds it in this process's. A relative directory, which
// exec.LookPath refuses to run from, is passed over.
func lookPath(e env.Env, name string) (string, error) {
	for _, dir := range filepath.SplitList(e.Get("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return path, nil
		}
	}
	return name, &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// Token is $GH_TOKEN or $GITHUB_TOKEN of ctx's Env, or else what `gh auth
// token` prints, run once in this process, with the Env of the first ctx
// that needs it.
func Token(ctx context.Context) (string, error) {
	e := env.From(ctx)
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := e.Get(name); v != "" {
			return v, nil
		}
	}
	tokenOnce.Do(func() {
		out, err := Command(ctx, "auth", "token").Output()
		if err != nil {
			tokenErr = fmt.Errorf("no GitHub login: `gh auth token` failed: %w", err)
			return
		}
		token = strings.TrimSpace(string(out))
	})
	return token, tokenErr
}

// defaultVersion is the REST API version every call but RESTVersion's asks
// for.
const defaultVersion = "2022-11-28"

func do(ctx context.Context, method, url string, body any, accept, version string, transport *http.Client, limit int64) ([]byte, error) {
	tok, err := Token(ctx)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := transport.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var source io.Reader = resp.Body
	if limit > 0 {
		source = io.LimitReader(source, limit+1)
	}
	data, err := io.ReadAll(source)
	if err != nil {
		return nil, err
	}
	if limit > 0 && int64(len(data)) > limit {
		return nil, fmt.Errorf("GitHub response exceeds %d bytes; no partial content returned", limit)
	}
	if resp.StatusCode >= 300 {
		return nil, &StatusError{resp.StatusCode, fmt.Sprintf("%s %s: %s: %s", method, strings.TrimPrefix(url, api), resp.Status, strings.TrimSpace(string(data)))}
	}
	return data, nil
}

// StatusError is a reply from GitHub with a status of 300 or above.
type StatusError struct {
	Code int
	msg  string
}

func (e *StatusError) Error() string { return e.msg }

var slugURL = regexp.MustCompile(`^(?:git@github\.com:|ssh://git@github\.com/|https://github\.com/)([^/]+)/(.+?)(?:\.git)?/?$`)

// Slug returns owner/name for a GitHub remote URL, or "" for any other URL.
func Slug(url string) string {
	if m := slugURL.FindStringSubmatch(url); m != nil {
		return m[1] + "/" + m[2]
	}
	return ""
}

// REST sends a JSON body (nil for none) and decodes the reply into out (nil
// to discard). Every call here authenticates with Token(ctx).
func REST(ctx context.Context, method, path string, body, out any) error {
	return RESTVersion(ctx, defaultVersion, method, path, body, out)
}

// RESTVersion is REST for an endpoint GitHub serves only under a later API
// version, named in GitHub's documentation of the endpoint.
func RESTVersion(ctx context.Context, version, method, path string, body, out any) error {
	data, err := do(ctx, method, api+"/"+strings.TrimPrefix(path, "/"), body, "application/vnd.github+json", version, client, 0)
	if err != nil || out == nil || len(data) == 0 {
		return err
	}
	return json.Unmarshal(data, out)
}

// Raw returns a GET response body as is, following redirects.
func Raw(ctx context.Context, path string) ([]byte, error) {
	return do(ctx, "GET", api+"/"+strings.TrimPrefix(path, "/"), nil, "application/vnd.github+json", defaultVersion, client, 0)
}

// Read returns at most 16 MiB from an API path, refusing redirects and
// oversized responses instead of following URLs or returning partial content.
func Read(ctx context.Context, path, accept string) ([]byte, error) {
	transport := *client
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return do(ctx, "GET", api+"/"+strings.TrimPrefix(path, "/"), nil, accept, defaultVersion, &transport, 16<<20)
}

// GraphQL runs a query and decodes its data into out.
func GraphQL(ctx context.Context, query string, variables map[string]any, out any) error {
	data, err := do(ctx, "POST", api+"/graphql", map[string]any{"query": query, "variables": variables}, "application/json", defaultVersion, client, 0)
	if err != nil {
		return err
	}
	var reply struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return err
	}
	if len(reply.Errors) > 0 {
		messages := make([]string, len(reply.Errors))
		for i, e := range reply.Errors {
			messages[i] = e.Message
		}
		return fmt.Errorf("GraphQL: %s", strings.Join(messages, "; "))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(reply.Data, out)
}
