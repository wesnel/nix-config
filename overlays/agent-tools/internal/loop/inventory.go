package loop

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/gh"
)

type InventoryRepository struct {
	ID       int64  `json:"id"`
	FullName string `json:"fullName"`
}

type InventoryRef struct {
	Ref        string               `json:"ref"`
	SHA        string               `json:"sha"`
	Repository *InventoryRepository `json:"repository"`
}

type InventoryPR struct {
	Number  int          `json:"number"`
	Title   string       `json:"title"`
	URL     string       `json:"url"`
	Author  string       `json:"author"`
	IsDraft bool         `json:"isDraft"`
	Head    InventoryRef `json:"head"`
	Base    InventoryRef `json:"base"`
}

type PRInventory struct {
	Repository   string        `json:"repository"`
	PullRequests []InventoryPR `json:"pullRequests"`
}

var inventorySlug = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
var inventorySHA = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

type inventoryWireRepository struct {
	FullDatabaseID string `json:"fullDatabaseId"`
	NameWithOwner  string
}

type inventoryWirePR struct {
	Number                  int
	Title, URL, State       string
	IsDraft                 *bool
	Author                  json.RawMessage
	HeadRefName, HeadRefOID string
	HeadRepository          json.RawMessage
	BaseRefName, BaseRefOID string
	BaseRepository          json.RawMessage
}

type inventoryPage struct {
	Nodes    []*inventoryWirePR
	PageInfo *struct {
		HasNextPage *bool
		EndCursor   *string
	}
}

const inventoryQuery = `query($owner: String!, $name: String!, $after: String) {
	repository(owner: $owner, name: $name) {
		nameWithOwner
		pullRequests(first: 100, after: $after, states: [OPEN], orderBy: {field: CREATED_AT, direction: ASC}) {
			nodes {
				number title url state isDraft author { login }
				headRefName headRefOid headRepository { fullDatabaseId nameWithOwner }
				baseRefName baseRefOid baseRepository { fullDatabaseId nameWithOwner }
			}
			pageInfo { hasNextPage endCursor }
		}
	}
}`

// Inventory traverses the repository's OPEN cursor connection without a checkout
// or loop mode. Errors return no inventory. The bound counts open-PR pages;
// concurrent changes on GitHub are not an atomic snapshot.
func Inventory(repo string) (*PRInventory, error) {
	if !inventorySlug.MatchString(repo) {
		return nil, fmt.Errorf("repository must be OWNER/REPO")
	}
	result := &PRInventory{Repository: repo, PullRequests: []InventoryPR{}}
	seenPRs, seenCursors := map[int]bool{}, map[string]bool{}
	var after *string
	for count := 0; count < 1000; count++ {
		page, err := readInventoryPage(repo, after)
		if err != nil {
			return nil, err
		}
		for _, row := range page.Nodes {
			pr, err := row.pullRequest(repo)
			if err != nil {
				return nil, err
			}
			if seenPRs[pr.Number] {
				return nil, fmt.Errorf("duplicate pull request #%d across inventory pages; retry inventory", pr.Number)
			}
			seenPRs[pr.Number] = true
			result.PullRequests = append(result.PullRequests, pr)
		}
		if !*page.PageInfo.HasNextPage {
			return result, nil
		}
		cursor := *page.PageInfo.EndCursor
		if seenCursors[cursor] {
			return nil, fmt.Errorf("repeated inventory cursor; no partial inventory returned")
		}
		seenCursors[cursor] = true
		after = &cursor
	}
	return nil, fmt.Errorf("pull request inventory exceeded 1000 open-PR pages; no partial inventory returned")
}

func readInventoryPage(repo string, after *string) (*inventoryPage, error) {
	owner, name, _ := strings.Cut(repo, "/")
	var data struct {
		Repository *struct {
			NameWithOwner string
			PullRequests  *inventoryPage
		}
	}
	if err := gh.GraphQL(inventoryQuery, map[string]any{"owner": owner, "name": name, "after": after}, &data); err != nil {
		return nil, err
	}
	if data.Repository == nil || !strings.EqualFold(data.Repository.NameWithOwner, repo) || data.Repository.PullRequests == nil {
		return nil, fmt.Errorf("missing or different repository in inventory")
	}
	page := data.Repository.PullRequests
	if page.Nodes == nil || len(page.Nodes) > 100 || page.PageInfo == nil || page.PageInfo.HasNextPage == nil {
		return nil, fmt.Errorf("incomplete inventory page")
	}
	if *page.PageInfo.HasNextPage && (len(page.Nodes) == 0 || page.PageInfo.EndCursor == nil || strings.TrimSpace(*page.PageInfo.EndCursor) == "") {
		return nil, fmt.Errorf("inventory page has no continuation cursor or nodes")
	}
	return page, nil
}

func (row *inventoryWirePR) pullRequest(repo string) (InventoryPR, error) {
	if row == nil || row.Number <= 0 || row.Title == "" || row.URL == "" || row.State != "OPEN" || row.IsDraft == nil {
		return InventoryPR{}, fmt.Errorf("incomplete or non-open pull request in inventory")
	}
	author, err := inventoryAuthor(row.Author)
	if err != nil {
		return InventoryPR{}, err
	}
	head, err := inventoryReference(row.HeadRefName, row.HeadRefOID, row.HeadRepository)
	if err != nil {
		return InventoryPR{}, err
	}
	base, err := inventoryReference(row.BaseRefName, row.BaseRefOID, row.BaseRepository)
	if err != nil {
		return InventoryPR{}, err
	}
	if base.Repository == nil || !strings.EqualFold(base.Repository.FullName, repo) {
		return InventoryPR{}, fmt.Errorf("pull request #%d has a different or missing base repository", row.Number)
	}
	return InventoryPR{Number: row.Number, Title: row.Title, URL: row.URL, Author: author, IsDraft: *row.IsDraft, Head: head, Base: base}, nil
}

func inventoryAuthor(raw json.RawMessage) (string, error) {
	var author *struct{ Login string }
	if err := json.Unmarshal(raw, &author); err != nil {
		return "", fmt.Errorf("invalid inventory author: %w", err)
	}
	if author == nil {
		return "", nil
	}
	if author.Login == "" {
		return "", fmt.Errorf("inventory author has no login")
	}
	return author.Login, nil
}

func inventoryReference(ref, oid string, raw json.RawMessage) (InventoryRef, error) {
	if ref == "" || !inventorySHA.MatchString(oid) {
		return InventoryRef{}, fmt.Errorf("incomplete pull request reference in inventory")
	}
	var repository *inventoryWireRepository
	if err := json.Unmarshal(raw, &repository); err != nil {
		return InventoryRef{}, fmt.Errorf("invalid inventory repository: %w", err)
	}
	result := InventoryRef{Ref: ref, SHA: oid}
	if repository != nil {
		id, err := strconv.ParseInt(repository.FullDatabaseID, 10, 64)
		if err != nil || id <= 0 || !inventorySlug.MatchString(repository.NameWithOwner) {
			return InventoryRef{}, fmt.Errorf("invalid pull request repository identity in inventory")
		}
		result.Repository = &InventoryRepository{ID: id, FullName: repository.NameWithOwner}
	}
	return result, nil
}
