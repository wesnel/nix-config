package loop

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gh"
)

func inventoryPull(number int) map[string]any {
	return map[string]any{"number": number, "title": "A change", "html_url": fmt.Sprintf("https://github.com/o/r/pull/%d", number), "state": "open", "draft": number == 1,
		"user": map[string]any{"login": "someone"},
		"head": map[string]any{"ref": "feature", "sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "repo": map[string]any{"id": 2, "full_name": "fork/r"}},
		"base": map[string]any{"ref": "main", "sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "repo": map[string]any{"id": 1, "full_name": "o/r"}}}
}

// GitHub is external; the real HTTP client reads only this test's server.
func inventoryServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv(ModeEnv, "")
	t.Chdir(t.TempDir())
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Cleanup(gh.SetAPI(server.URL))
}

func TestInventory(t *testing.T) {
	t.Run("closure on an earlier page does not skip an open PR", func(t *testing.T) {
		closed := false
		inventoryServer(t, func(w http.ResponseWriter, r *http.Request) {
			rows := []any{}
			for n := 1; n <= 102; n++ {
				row := inventoryPull(n)
				if n == 1 && closed {
					row["state"] = "closed"
					if r.URL.Query().Get("state") == "open" {
						continue
					}
				}
				rows = append(rows, row)
			}
			if r.URL.Query().Get("page") == "1" {
				rows = rows[:100]
				closed = true
			} else {
				rows = rows[100:]
			}
			json.NewEncoder(w).Encode(rows)
		})
		got, err := Inventory("o/r")
		if err != nil {
			t.Fatal(err)
		}
		for _, pr := range got.PullRequests {
			if pr.Number == 101 {
				return
			}
		}
		t.Fatal("continuously-open PR 101 was skipped when PR 1 closed")
	})
	t.Run("closed history needs only identity and state", func(t *testing.T) {
		inventoryServer(t, func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode([]any{map[string]any{"number": 1, "state": "closed"}, inventoryPull(2)})
		})
		got, err := Inventory("o/r")
		if err != nil || len(got.PullRequests) != 1 || got.PullRequests[0].Number != 2 {
			t.Fatalf("open inventory %+v, error %v", got, err)
		}
	})
	t.Run("invalid repository never requests GitHub", func(t *testing.T) {
		inventoryServer(t, func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("invalid repository reached GitHub: %s", r.URL)
			fmt.Fprint(w, "[]")
		})
		for _, repo := range []string{"", "o", "o/r/extra", "../r", "o/r?state=closed", "https://github.com/o/r", "o/r\n"} {
			got, err := Inventory(repo)
			if err == nil || got != nil {
				t.Errorf("Inventory(%q) = %+v, %v; want no inventory and error", repo, got, err)
			}
		}
	})
	t.Run("empty repository is a JSON list", func(t *testing.T) {
		inventoryServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "[]") })
		got, err := Inventory("o/r")
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(got)
		if err != nil || string(data) != `{"repository":"o/r","pullRequests":[]}` {
			t.Fatalf("empty inventory = %s, %v", data, err)
		}
	})
	t.Run("deleted fork remains in inventory", func(t *testing.T) {
		inventoryServer(t, func(w http.ResponseWriter, r *http.Request) {
			row := inventoryPull(1)
			row["head"].(map[string]any)["repo"] = nil
			json.NewEncoder(w).Encode([]any{row})
		})
		got, err := Inventory("o/r")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.PullRequests) != 1 || got.PullRequests[0].Head.Repository != nil || got.PullRequests[0].Head.Ref != "feature" {
			t.Fatalf("deleted fork: %+v", got)
		}
	})
	t.Run("later error discards earlier pages", func(t *testing.T) {
		for _, failure := range []string{"http error", "malformed JSON", "null page", "object page", "duplicate number", "oversized page", "incomplete PR", "invalid state", "missing draft", "missing author", "missing base", "wrong base", "invalid SHA", "invalid repository"} {
			t.Run(failure, func(t *testing.T) {
				requests := 0
				inventoryServer(t, func(w http.ResponseWriter, r *http.Request) {
					requests++
					if r.URL.Query().Get("page") == "1" {
						rows := []any{}
						for n := 1; n <= 100; n++ {
							rows = append(rows, inventoryPull(n))
						}
						json.NewEncoder(w).Encode(rows)
						return
					}
					row := inventoryPull(101)
					switch failure {
					case "http error":
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					case "malformed JSON":
						fmt.Fprint(w, "[")
						return
					case "null page":
						fmt.Fprint(w, "null")
						return
					case "object page":
						fmt.Fprint(w, "{}")
						return
					case "duplicate number":
						row["number"] = 1
					case "oversized page":
						rows := []any{}
						for n := 101; n <= 201; n++ {
							rows = append(rows, inventoryPull(n))
						}
						json.NewEncoder(w).Encode(rows)
						return
					case "incomplete PR":
						delete(row, "number")
					case "invalid state":
						row["state"] = "unknown"
					case "missing draft":
						delete(row, "draft")
					case "missing author":
						row["user"] = nil
					case "missing base":
						row["base"].(map[string]any)["repo"] = nil
					case "wrong base":
						row["base"].(map[string]any)["repo"].(map[string]any)["full_name"] = "other/r"
					case "invalid SHA":
						row["head"].(map[string]any)["sha"] = "bad"
					case "invalid repository":
						row["head"].(map[string]any)["repo"].(map[string]any)["id"] = 0
					}
					json.NewEncoder(w).Encode([]any{row})
				})
				got, err := Inventory("o/r")
				if err == nil || got != nil || requests != 2 {
					t.Fatalf("got %+v, %v after %d requests; want no inventory and error after second page", got, err, requests)
				}
			})
		}
	})
	t.Run("pagination limit refuses partial inventory", func(t *testing.T) {
		requests := 0
		inventoryServer(t, func(w http.ResponseWriter, r *http.Request) {
			rows := []any{}
			for n := requests*100 + 1; n <= (requests+1)*100; n++ {
				rows = append(rows, inventoryPull(n))
			}
			requests++
			json.NewEncoder(w).Encode(rows)
		})
		got, err := Inventory("o/r")
		if got != nil || err == nil || !strings.Contains(err.Error(), "exceeded") || requests != 1000 {
			t.Fatalf("limit: inventory nil=%t, error=%v, requests=%d", got == nil, err, requests)
		}
	})
	t.Run("all authors drafts and fork branches across pages", func(t *testing.T) {
		inventoryServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/repos/o/r/pulls" || r.URL.Query().Get("state") != "all" || r.URL.Query().Get("per_page") != "100" {
				t.Errorf("unexpected request: %s", r.URL)
			}
			rows := []any{}
			switch r.URL.Query().Get("page") {
			case "1":
				for n := 1; n <= 100; n++ {
					rows = append(rows, inventoryPull(n))
				}
			case "2":
				row := inventoryPull(101)
				row["user"] = map[string]any{"login": "another"}
				row["head"].(map[string]any)["repo"] = map[string]any{"id": 3, "full_name": "another/r"}
				rows = append(rows, row)
			default:
				t.Errorf("unexpected page: %s", r.URL)
			}
			json.NewEncoder(w).Encode(rows)
		})
		got, err := Inventory("o/r")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.PullRequests) != 101 {
			t.Fatalf("got %d pull requests, want 101", len(got.PullRequests))
		}
		first, last := got.PullRequests[0], got.PullRequests[100]
		if got.Repository != "o/r" || !first.IsDraft || first.Author != "someone" || last.Author != "another" || last.Number != 101 || first.Head.Repository.FullName != "fork/r" || last.Head.Repository.FullName != "another/r" || first.Head.Ref != "feature" || last.Head.Ref != "feature" || first.Base.Ref != "main" || first.Base.SHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || first.Head.SHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || first.Title != "A change" || first.URL != "https://github.com/o/r/pull/1" {
			t.Fatalf("wrong inventory: %+v; first %+v; last %+v", got, first, last)
		}
	})
}
