package loop

import (
	"encoding/json"
	"fmt"
	"regexp"
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

type inventoryWireRef struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo *struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repo"`
}

type inventoryWirePR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"html_url"`
	State  string `json:"state"`
	Draft  *bool  `json:"draft"`
	User   *struct {
		Login string `json:"login"`
	} `json:"user"`
	Head inventoryWireRef `json:"head"`
	Base inventoryWireRef `json:"base"`
}

// Inventory reads all open pull requests, including drafts and forks, without
// a checkout or loop mode. It scans history in creation order so closures cannot
// shift page offsets, returning rows observed open. An error returns no inventory.
// Pagination is bounded at 1000 pages; concurrent changes are not a snapshot.
func Inventory(repo string) (*PRInventory, error) {
	if !inventorySlug.MatchString(repo) {
		return nil, fmt.Errorf("repository must be OWNER/REPO")
	}
	result := &PRInventory{Repository: repo, PullRequests: []InventoryPR{}}
	seen := map[int]bool{}
	for page := 1; page <= 1000; page++ {
		path := fmt.Sprintf("repos/%s/pulls?state=all&sort=created&direction=asc&per_page=100&page=%d", repo, page)
		data, err := gh.Read(path, "application/vnd.github+json")
		if err != nil {
			return nil, err
		}
		var rows []inventoryWirePR
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, fmt.Errorf("pull request inventory page %d: %w", page, err)
		}
		if rows == nil || len(rows) > 100 {
			return nil, fmt.Errorf("invalid pull request inventory page %d", page)
		}
		if err := appendInventoryPage(result, seen, rows); err != nil {
			return nil, err
		}
		if len(rows) < 100 {
			return result, nil
		}
	}
	return nil, fmt.Errorf("pull request inventory exceeded 1000 pages; no partial inventory returned")
}

func appendInventoryPage(result *PRInventory, seen map[int]bool, rows []inventoryWirePR) error {
	for _, row := range rows {
		if row.Number <= 0 || row.State != "open" && row.State != "closed" {
			return fmt.Errorf("invalid pull request identity or state in inventory")
		}
		if seen[row.Number] {
			return fmt.Errorf("duplicate pull request #%d across inventory pages; retry inventory", row.Number)
		}
		seen[row.Number] = true
		if row.State == "closed" {
			continue
		}
		pr, err := row.pullRequest(result.Repository)
		if err != nil {
			return err
		}
		result.PullRequests = append(result.PullRequests, pr)
	}
	return nil
}

func (row inventoryWirePR) pullRequest(repo string) (InventoryPR, error) {
	if row.Number <= 0 || row.Title == "" || row.URL == "" || row.State != "open" || row.Draft == nil || row.User == nil || row.User.Login == "" {
		return InventoryPR{}, fmt.Errorf("incomplete pull request #%d in inventory", row.Number)
	}
	head, err := row.Head.reference()
	if err != nil {
		return InventoryPR{}, err
	}
	base, err := row.Base.reference()
	if err != nil {
		return InventoryPR{}, err
	}
	if base.Repository == nil || !strings.EqualFold(base.Repository.FullName, repo) {
		return InventoryPR{}, fmt.Errorf("pull request #%d has a different or missing base repository", row.Number)
	}
	return InventoryPR{Number: row.Number, Title: row.Title, URL: row.URL, Author: row.User.Login, IsDraft: *row.Draft, Head: head, Base: base}, nil
}

func (ref inventoryWireRef) reference() (InventoryRef, error) {
	if ref.Ref == "" || !inventorySHA.MatchString(ref.SHA) {
		return InventoryRef{}, fmt.Errorf("incomplete pull request reference in inventory")
	}
	result := InventoryRef{Ref: ref.Ref, SHA: ref.SHA}
	if ref.Repo != nil {
		if ref.Repo.ID <= 0 || !inventorySlug.MatchString(ref.Repo.FullName) {
			return InventoryRef{}, fmt.Errorf("invalid pull request repository in inventory")
		}
		result.Repository = &InventoryRepository{ID: ref.Repo.ID, FullName: ref.Repo.FullName}
	}
	return result, nil
}
