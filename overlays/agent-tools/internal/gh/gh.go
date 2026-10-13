// Package gh talks to the GitHub API with the user's existing `gh` login.
package gh

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const api = "https://api.github.com"

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

// Token is $GH_TOKEN, $GITHUB_TOKEN, or what `gh auth token` prints.
func Token() (string, error) {
	tokenOnce.Do(func() {
		for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
			if v := os.Getenv(name); v != "" {
				token = v
				return
			}
		}
		cmd := exec.Command("gh", "auth", "token")
		cmd.Dir = Outside
		out, err := cmd.Output()
		if err != nil {
			tokenErr = fmt.Errorf("no GitHub login: `gh auth token` failed: %w", err)
			return
		}
		token = strings.TrimSpace(string(out))
	})
	return token, tokenErr
}

func do(method, url string, body any, accept string) ([]byte, error) {
	tok, err := Token()
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
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: %s: %s", method, strings.TrimPrefix(url, api), resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// REST sends a JSON body (nil for none) and decodes the reply into out (nil to discard).
func REST(method, path string, body, out any) error {
	data, err := do(method, api+"/"+strings.TrimPrefix(path, "/"), body, "application/vnd.github+json")
	if err != nil || out == nil || len(data) == 0 {
		return err
	}
	return json.Unmarshal(data, out)
}

// Raw returns a GET response body as is, following redirects.
func Raw(path string) ([]byte, error) {
	return do("GET", api+"/"+strings.TrimPrefix(path, "/"), nil, "application/vnd.github+json")
}

// GraphQL runs a query and decodes its data into out.
func GraphQL(query string, variables map[string]any, out any) error {
	data, err := do("POST", api+"/graphql", map[string]any{"query": query, "variables": variables}, "application/json")
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
