package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// GitHub is external; the real MCP process instead reads this authenticated server.
func githubReadClient(t *testing.T, handler http.HandlerFunc) *mcpClient {
	t.Helper()
	f := newMCPFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+fakeToken {
			t.Errorf("unexpected request: %s %s or missing test login", r.Method, r.URL.Path)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	for i, entry := range f.env {
		if strings.HasPrefix(entry, serveMCPEnv+"=") {
			f.env[i] = serveMCPEnv + "=" + server.URL
		}
	}
	return f.start(t)
}

func TestGitHubRead(t *testing.T) {
	t.Run("missing view data fails", func(t *testing.T) {
		for _, missing := range []string{"metadata", "page"} {
			t.Run(missing, func(t *testing.T) {
				c := githubReadClient(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/repos/o/r/pulls/1" {
						if missing == "metadata" {
							io.WriteString(w, "null")
						} else {
							io.WriteString(w, `{"title":"a"}`)
						}
					} else if missing == "page" {
						io.WriteString(w, "null")
					} else {
						io.WriteString(w, "[]")
					}
				})
				r := c.tool(t, "github_pr_view", map[string]any{"repo": "o/r", "number": 1})
				if !r.isError || r.out != "" {
					t.Fatalf("missing data reported as complete: %+v", r)
				}
			})
		}
	})

	t.Run("refuse invalid arguments before request", func(t *testing.T) {
		var calls atomic.Int32
		c := githubReadClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Error(w, "unexpected request", http.StatusBadRequest)
		})
		for _, tc := range []struct {
			name, tool, key string
			value           any
		}{
			{"host", "github_pr_diff", "repo", "https://evil.test/o/r"},
			{"query", "github_pr_view", "repo", "o/r?x=y"},
			{"parent repo", "github_file", "repo", "o/.."},
			{"zero", "github_pr_diff", "number", 0},
			{"negative", "github_pr_view", "number", -1},
			{"fractional", "github_pr_diff", "number", 1.5},
			{"branch", "github_file", "commit", "main"},
			{"short sha", "github_file", "commit", "abcdef0"},
			{"absolute", "github_file", "path", "/etc/passwd"},
			{"parent path", "github_file", "path", "a/../b"},
			{"empty path", "github_file", "path", ""},
			{"empty component", "github_file", "path", "a//b"},
			{"backslash", "github_file", "path", `a\b`},
			{"nul", "github_file", "path", "a\x00b"},
			{"shell", "github_pr_view", "command", "echo ignored"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				args := map[string]any{"repo": "o/r", "number": 1}
				if tc.tool == "github_file" {
					args = map[string]any{"repo": "o/r", "commit": strings.Repeat("a", 40), "path": "a"}
				}
				args[tc.key] = tc.value
				r := c.tool(t, tc.tool, args)
				if !r.isError || !strings.Contains(r.text, tc.key) {
					t.Errorf("result %+v must refuse %s", r, tc.key)
				}
			})
		}
		if calls.Load() != 0 {
			t.Fatalf("invalid arguments reached GitHub")
		}
	})

	t.Run("diff failures return no partial content", func(t *testing.T) {
		for _, scenario := range []string{"base moves", "head moves", "missing base", "missing head", "missing file", "not a diff", "invalid UTF8", "http error", "oversized", "redirect"} {
			t.Run(scenario, func(t *testing.T) {
				metadata := 0
				var redirected atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					redirected.Add(1)
				}))
				t.Cleanup(target.Close)
				c := githubReadClient(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Accept") != "application/vnd.github.diff" {
						metadata++
						base, head := "base", "head"
						if metadata == 2 && scenario == "base moves" {
							base = "new base"
						}
						if metadata == 2 && scenario == "head moves" {
							head = "new head"
						}
						if scenario == "missing base" {
							base = ""
						}
						if scenario == "missing head" {
							head = ""
						}
						fmt.Fprintf(w, `{"base":{"sha":%q},"head":{"sha":%q},"changed_files":1}`, base, head)
						return
					}
					switch scenario {
					case "http error":
						http.Error(w, "not available", http.StatusForbidden)
					case "redirect":
						http.Redirect(w, r, target.URL, http.StatusFound)
					case "oversized":
						io.WriteString(w, "diff --git a/a b/a\n"+strings.Repeat("x", 16<<20))
					case "missing file":
					case "not a diff":
						io.WriteString(w, "unexpected prefix\ndiff --git a/a b/a\n")
					case "invalid UTF8":
						io.WriteString(w, "diff --git a/a b/a\n\xff")
					default:
						io.WriteString(w, "diff --git a/a b/a\n")
					}
				})
				r := c.tool(t, "github_pr_diff", map[string]any{"repo": "o/r", "number": 1})
				if !r.isError || r.out != "" || metadata == 0 {
					t.Fatalf("expected error without partial diff: isError=%t, stdout bytes=%d, metadata calls=%d", r.isError, len(r.out), metadata)
				}
				if scenario == "oversized" && !strings.Contains(r.text, "exceeds 16777216 bytes") {
					t.Errorf("expected size refusal: %s", r.text)
				}
				if redirected.Load() != 0 {
					t.Error("followed redirect")
				}
			})
		}
	})

	t.Run("diff at response limit", func(t *testing.T) {
		const header = "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -0,0 +1 @@\n+"
		diff := header + strings.Repeat("x", (16<<20)-len(header)-1) + "\n"
		c := githubReadClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Accept") == "application/vnd.github.diff" {
				io.WriteString(w, diff)
				return
			}
			io.WriteString(w, `{"base":{"sha":"base"},"head":{"sha":"head"},"changed_files":1}`)
		})
		r := c.tool(t, "github_pr_diff", map[string]any{"repo": "o/r", "number": 1})
		var got struct{ Diff string }
		if err := json.Unmarshal([]byte(r.out), &got); err != nil || r.isError || got.Diff != diff {
			t.Fatalf("exact limit rejected or changed: isError=%t, stdout bytes=%d, decode error=%v", r.isError, len(r.out), err)
		}
	})

	t.Run("later page failure discards partial view", func(t *testing.T) {
		lastPage := false
		c := githubReadClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/repos/o/r/pulls/1" {
				io.WriteString(w, `{"title":"a"}`)
				return
			}
			if r.URL.Query().Get("page") == "1" {
				json.NewEncoder(w).Encode(make([]map[string]string, 100))
				return
			}
			lastPage = true
			http.Error(w, "later page failed", http.StatusBadGateway)
		})
		r := c.tool(t, "github_pr_view", map[string]any{"repo": "o/r", "number": 1})
		if !lastPage || !r.isError || r.out != "" || !strings.Contains(r.text, "later page failed") {
			t.Fatalf("partial view: %+v", r)
		}
	})

	t.Run("unsupported file responses", func(t *testing.T) {
		for _, tc := range []struct {
			name, field string
			value       any
			says        string
		}{
			{"directory array", "", []any{}, "unmarshal"},
			{"non-file", "type", "dir", "complete file"},
			{"omitted encoding", "encoding", "none", "complete file"},
			{"submodule", "submodule_git_url", "https://example.test/repo", "complete file"},
			{"missing blob identity", "sha", "", "complete file"},
			{"missing size", "size", nil, "complete file"},
			{"size mismatch", "size", 5, "incomplete file"},
			{"invalid base64", "content", "aGVsbG8K!", "invalid"},
			{"binary", "content", "aGVsbG8A", "binary"},
			{"invalid UTF8", "content", "aGVsbG//", "UTF-8"},
			{"not found", "status", 404, "404"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				called := false
				c := githubReadClient(t, func(w http.ResponseWriter, r *http.Request) {
					called = true
					if tc.field == "status" {
						http.NotFound(w, r)
						return
					}
					file := map[string]any{"type": "file", "encoding": "base64", "sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "size": 6, "content": "aGVsbG8K"}
					file[tc.field] = tc.value
					if tc.field == "" {
						json.NewEncoder(w).Encode(tc.value)
					} else {
						json.NewEncoder(w).Encode(file)
					}
				})
				r := c.tool(t, "github_file", map[string]any{"repo": "o/r", "commit": strings.Repeat("a", 40), "path": "a"})
				if !called || !r.isError || r.out != "" || !strings.Contains(r.text, tc.says) {
					t.Fatalf("result %+v, want %s", r, tc.says)
				}
			})
		}
	})

	t.Run("view includes later discussion pages", func(t *testing.T) {
		c := githubReadClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repos/o/r/pulls/893":
				io.WriteString(w, `{"title":"Read me","body":"Full description","head":{"sha":"head"}}`)
			case "/repos/o/r/issues/893/comments", "/repos/o/r/pulls/893/reviews", "/repos/o/r/pulls/893/comments":
				if r.URL.Query().Get("per_page") != "100" {
					t.Errorf("missing page size: %s", r.URL)
				}
				if r.URL.Query().Get("page") == "1" {
					comments := make([]map[string]any, 100)
					for i := range comments {
						comments[i] = map[string]any{"body": "first page", "user": nil}
					}
					json.NewEncoder(w).Encode(comments)
				} else {
					io.WriteString(w, `[{"body":"last page"}]`)
				}
			default:
				t.Errorf("unexpected path: %s", r.URL.Path)
				http.NotFound(w, r)
			}
		})
		r := c.tool(t, "github_pr_view", map[string]any{"repo": "o/r", "number": 893})
		var got struct {
			PR       struct{ Title, Body string } `json:"pull_request"`
			Comments []struct{ Body string }      `json:"comments"`
			Reviews  []struct{ Body string }      `json:"reviews"`
			Inline   []struct{ Body string }      `json:"review_comments"`
		}
		if err := json.Unmarshal([]byte(r.out), &got); err != nil || r.isError || got.PR.Title != "Read me" || got.PR.Body != "Full description" {
			t.Fatalf("view result %+v: %v", r, err)
		}
		for _, list := range [][]struct{ Body string }{got.Comments, got.Reviews, got.Inline} {
			if len(list) != 101 || list[100].Body != "last page" {
				t.Errorf("discussion not complete: %+v", list)
			}
		}
	})

	t.Run("file at commit preserves text and escapes path", func(t *testing.T) {
		for _, path := range []string{"dir/a ?#.txt", "-notes.md"} {
			t.Run(path, func(t *testing.T) {
				c := githubReadClient(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/repos/o/r/contents/"+path || r.URL.Query().Get("ref") != strings.Repeat("a", 40) || len(r.URL.Query()) != 1 {
						t.Errorf("wrong file request: %s", r.URL)
					}
					fmt.Fprintf(w, `{"type":"file","encoding":"base64","size":6,"sha":%q,"content":"aGVsbG8K"}`, strings.Repeat("b", 40))
				})
				r := c.tool(t, "github_file", map[string]any{"repo": "o/r", "commit": strings.Repeat("a", 40), "path": path})
				var got struct{ Content, SHA, Commit, Path string }
				if err := json.Unmarshal([]byte(r.out), &got); err != nil || r.isError || got.Content != "hello\n" || got.SHA != strings.Repeat("b", 40) || got.Commit != strings.Repeat("a", 40) || got.Path != path {
					t.Fatalf("file result %+v: %v", r, err)
				}
			})
		}
	})

	t.Run("diff without checkout", func(t *testing.T) {
		const diff = "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n"
		metadata := 0
		c := githubReadClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/repos/o/r/pulls/893" {
				t.Errorf("path = %s", r.URL.Path)
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("Accept") == "application/vnd.github.diff" {
				io.WriteString(w, diff)
				return
			}
			metadata++
			fmt.Fprint(w, `{"base":{"sha":"base"},"head":{"sha":"head"},"changed_files":1}`)
		})
		r := c.tool(t, "github_pr_diff", map[string]any{"repo": "o/r", "number": 893})
		var got struct {
			Diff string `json:"diff"`
			Base string `json:"base_sha"`
			Head string `json:"head_sha"`
		}
		if err := json.Unmarshal([]byte(r.out), &got); err != nil || r.isError || got.Diff != diff || got.Base != "base" || got.Head != "head" || metadata != 2 {
			t.Fatalf("diff result %+v (metadata calls %d): %v", r, metadata, err)
		}
	})
}
