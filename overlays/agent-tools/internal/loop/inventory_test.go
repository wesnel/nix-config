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
	return map[string]any{"number": number, "title": "A change", "url": fmt.Sprintf("https://github.com/o/r/pull/%d", number), "state": "OPEN", "isDraft": number == 1,
		"author":      map[string]any{"login": "someone"},
		"headRefName": "feature", "headRefOid": strings.Repeat("a", 40),
		"headRepository": map[string]any{"fullDatabaseId": "4294967297", "nameWithOwner": "fork/r"},
		"baseRefName":    "main", "baseRefOid": strings.Repeat("b", 40),
		"baseRepository": map[string]any{"fullDatabaseId": "1", "nameWithOwner": "o/r"}}
}

func inventoryResponse(rows []any, more bool, cursor any) map[string]any {
	return map[string]any{"data": map[string]any{"repository": map[string]any{
		"nameWithOwner": "o/r", "pullRequests": map[string]any{
			"nodes": rows, "pageInfo": map[string]any{"hasNextPage": more, "endCursor": cursor},
		},
	}}}
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

func inventoryCursor(t *testing.T, r *http.Request) *string {
	t.Helper()
	var request struct {
		Query     string
		Variables struct {
			Owner, Name string
			After       *string
		}
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Errorf("request body: %v", err)
	}
	if r.Method != http.MethodPost || r.URL.Path != "/graphql" || request.Variables.Owner != "o" || request.Variables.Name != "r" ||
		!strings.Contains(request.Query, "first: 100") || !strings.Contains(request.Query, "states: [OPEN]") ||
		!strings.Contains(request.Query, "after: $after") || strings.Contains(request.Query, "author:") {
		t.Errorf("unexpected inventory request %s %s: %+v", r.Method, r.URL, request)
	}
	return request.Variables.After
}

func TestInventory(t *testing.T) {
	t.Run("large closed history does not consume open inventory pages", func(t *testing.T) {
		requests := 0
		inventoryServer(t, func(w http.ResponseWriter, r *http.Request) {
			requests++
			if r.Method == http.MethodGet {
				rows := []any{}
				for n := (requests-1)*100 + 1; n <= requests*100; n++ {
					rows = append(rows, map[string]any{"number": n, "state": "closed"})
				}
				json.NewEncoder(w).Encode(rows)
				return
			}
			if cursor := inventoryCursor(t, r); cursor != nil {
				t.Errorf("unexpected cursor %q", *cursor)
			}
			json.NewEncoder(w).Encode(inventoryResponse([]any{inventoryPull(100001)}, false, nil))
		})
		got, err := Inventory("o/r")
		if err != nil || got == nil || len(got.PullRequests) != 1 || got.PullRequests[0].Number != 100001 || requests != 1 {
			t.Fatalf("inventory %+v, error %v, requests %d; want one open-connection page", got, err, requests)
		}
	})

	t.Run("opaque cursor survives closure before it", func(t *testing.T) {
		const cursor = "opaque:after/created-position+?="
		closed := false
		requests := 0
		inventoryServer(t, func(w http.ResponseWriter, r *http.Request) {
			requests++
			after := inventoryCursor(t, r)
			if after == nil {
				closed = true
				json.NewEncoder(w).Encode(inventoryResponse([]any{inventoryPull(1)}, true, cursor))
				return
			}
			if *after != cursor || !closed {
				t.Errorf("after %q, closed %t", *after, closed)
			}
			row := inventoryPull(2)
			row["author"] = map[string]any{"login": "another"}
			row["headRepository"] = map[string]any{"fullDatabaseId": "3", "nameWithOwner": "another/r"}
			json.NewEncoder(w).Encode(inventoryResponse([]any{row}, false, "last"))
		})
		got, err := Inventory("o/r")
		if err != nil || got == nil || len(got.PullRequests) != 2 || requests != 2 {
			t.Fatalf("inventory %+v, error %v, requests %d", got, err, requests)
		}
		first, last := got.PullRequests[0], got.PullRequests[1]
		if !first.IsDraft || first.Author != "someone" || last.Author != "another" || last.Number != 2 ||
			first.Head.Repository.ID != 4294967297 || first.Head.Repository.FullName != "fork/r" || last.Head.Repository.FullName != "another/r" ||
			first.Head.Ref != "feature" || first.Base.Ref != "main" || first.Base.SHA != strings.Repeat("b", 40) || first.Head.SHA != strings.Repeat("a", 40) ||
			first.Title != "A change" || first.URL != "https://github.com/o/r/pull/1" {
			t.Fatalf("wrong inventory: first %+v, last %+v", first, last)
		}
	})

	t.Run("invalid repository never requests GitHub", func(t *testing.T) {
		inventoryServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid repository reached GitHub") })
		for _, repo := range []string{"", "o", "o/r/extra", "../r", "o/r?state=closed", "https://github.com/o/r", "o/r\n"} {
			got, err := Inventory(repo)
			if err == nil || got != nil {
				t.Errorf("Inventory(%q) = %+v, %v", repo, got, err)
			}
		}
	})

	t.Run("empty repository is a JSON list", func(t *testing.T) {
		inventoryServer(t, func(w http.ResponseWriter, r *http.Request) {
			inventoryCursor(t, r)
			json.NewEncoder(w).Encode(inventoryResponse([]any{}, false, nil))
		})
		got, err := Inventory("o/r")
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(got)
		if err != nil || string(data) != `{"repository":"o/r","pullRequests":[]}` {
			t.Fatalf("empty inventory = %s, %v", data, err)
		}
	})

	t.Run("deleted author and fork remain in inventory", func(t *testing.T) {
		inventoryServer(t, func(w http.ResponseWriter, r *http.Request) {
			inventoryCursor(t, r)
			row := inventoryPull(1)
			row["author"], row["headRepository"] = nil, nil
			json.NewEncoder(w).Encode(inventoryResponse([]any{row}, false, nil))
		})
		got, err := Inventory("o/r")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.PullRequests) != 1 || got.PullRequests[0].Author != "" || got.PullRequests[0].Head.Repository != nil || got.PullRequests[0].Head.Ref != "feature" {
			t.Fatalf("deleted identities: %+v", got)
		}
	})

	t.Run("later error discards earlier pages", func(t *testing.T) {
		for _, failure := range []string{"http error", "malformed JSON", "GraphQL error", "null repository", "wrong repository", "missing connection",
			"null nodes", "missing nodes", "null node", "oversized page", "missing pageInfo", "missing hasNextPage", "missing cursor", "null cursor", "empty continuation", "repeated cursor",
			"duplicate number", "incomplete PR", "closed PR", "missing draft", "missing author", "empty author", "missing head repository",
			"missing base", "wrong base", "invalid SHA", "invalid repository", "invalid ID", "overflow ID", "numeric ID"} {
			t.Run(failure, func(t *testing.T) {
				requests := 0
				inventoryServer(t, func(w http.ResponseWriter, r *http.Request) {
					requests++
					if inventoryCursor(t, r) == nil {
						json.NewEncoder(w).Encode(inventoryResponse([]any{inventoryPull(1)}, true, "first-cursor"))
						return
					}
					row := inventoryPull(2)
					response := inventoryResponse([]any{row}, false, nil)
					data := response["data"].(map[string]any)
					repository := data["repository"].(map[string]any)
					connection := repository["pullRequests"].(map[string]any)
					switch failure {
					case "http error":
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					case "malformed JSON":
						fmt.Fprint(w, "[")
						return
					case "GraphQL error":
						response["errors"] = []any{map[string]any{"message": "denied"}}
					case "null repository":
						data["repository"] = nil
					case "wrong repository":
						repository["nameWithOwner"] = "other/r"
					case "missing connection":
						delete(repository, "pullRequests")
					case "null nodes":
						connection["nodes"] = nil
					case "missing nodes":
						delete(connection, "nodes")
					case "null node":
						connection["nodes"] = []any{nil}
					case "oversized page":
						rows := []any{}
						for n := 2; n <= 102; n++ {
							rows = append(rows, inventoryPull(n))
						}
						connection["nodes"] = rows
					case "missing pageInfo":
						delete(connection, "pageInfo")
					case "missing hasNextPage":
						connection["pageInfo"] = map[string]any{"endCursor": nil}
					case "missing cursor":
						connection["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": ""}
					case "null cursor":
						connection["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": nil}
					case "empty continuation":
						connection["nodes"] = []any{}
						connection["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": "next"}
					case "repeated cursor":
						connection["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": "first-cursor"}
					case "duplicate number":
						row["number"] = 1
					case "incomplete PR":
						delete(row, "number")
					case "closed PR":
						row["state"] = "CLOSED"
					case "missing draft":
						delete(row, "isDraft")
					case "missing author":
						delete(row, "author")
					case "empty author":
						row["author"] = map[string]any{"login": ""}
					case "missing head repository":
						delete(row, "headRepository")
					case "missing base":
						row["baseRepository"] = nil
					case "wrong base":
						row["baseRepository"].(map[string]any)["nameWithOwner"] = "other/r"
					case "invalid SHA":
						row["headRefOid"] = "bad"
					case "invalid repository":
						row["headRepository"].(map[string]any)["nameWithOwner"] = "bad"
					case "invalid ID":
						row["headRepository"].(map[string]any)["fullDatabaseId"] = "0"
					case "overflow ID":
						row["headRepository"].(map[string]any)["fullDatabaseId"] = "9223372036854775808"
					case "numeric ID":
						row["headRepository"].(map[string]any)["fullDatabaseId"] = 2
					}
					json.NewEncoder(w).Encode(response)
				})
				got, err := Inventory("o/r")
				if err == nil || got != nil || requests != 2 {
					t.Fatalf("got %+v, %v after %d requests", got, err, requests)
				}
			})
		}
	})

	t.Run("pagination limit refuses partial inventory", func(t *testing.T) {
		requests := 0
		inventoryServer(t, func(w http.ResponseWriter, r *http.Request) {
			inventoryCursor(t, r)
			requests++
			json.NewEncoder(w).Encode(inventoryResponse([]any{inventoryPull(requests)}, true, fmt.Sprint("cursor-", requests)))
		})
		got, err := Inventory("o/r")
		if got != nil || err == nil || !strings.Contains(err.Error(), "exceeded") || requests != 1000 {
			t.Fatalf("limit: inventory nil=%t, error=%v, requests=%d", got == nil, err, requests)
		}
	})
}
