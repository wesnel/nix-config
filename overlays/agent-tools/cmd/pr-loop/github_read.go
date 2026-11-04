package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/gh"
)

var githubRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+$`)
var githubCommit = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func githubRepoPath(repo string) (string, error) {
	if !githubRepo.MatchString(repo) || strings.HasSuffix(repo, "/.") || strings.HasSuffix(repo, "/..") {
		return "", fmt.Errorf("repo must be OWNER/NAME on github.com")
	}
	return "repos/" + repo, nil
}

func githubPRPath(argv []string) (string, error) {
	path, err := githubRepoPath(argv[0])
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(argv[1])
	if err != nil || n <= 0 {
		return "", fmt.Errorf("number must be a positive pull request number")
	}
	return path + "/pulls/" + argv[1], nil
}

func githubJSON(path string, out any) error {
	data, err := gh.Read(path, "application/vnd.github+json")
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

type githubPRIdentity struct {
	Base  struct{ SHA string } `json:"base"`
	Head  struct{ SHA string } `json:"head"`
	Files int                  `json:"changed_files"`
}

func githubPRDiff(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	path, err := githubPRPath(argv)
	var before, after githubPRIdentity
	var diff []byte
	if err == nil {
		err = githubJSON(path, &before)
	}
	if err == nil {
		diff, err = gh.Read(path, "application/vnd.github.diff")
	}
	if err == nil {
		err = githubJSON(path, &after)
	}
	if err == nil && (before.Base.SHA == "" || before.Head.SHA == "" || before != after) {
		err = fmt.Errorf("PR base or head changed or was missing; read the diff again")
	}
	if err == nil && (!utf8.Valid(diff) || strings.Count("\n"+string(diff), "\ndiff --git ") != before.Files ||
		(len(diff) != 0 && !strings.HasPrefix(string(diff), "diff --git "))) {
		err = fmt.Errorf("GitHub did not return a complete UTF-8 unified diff")
	}
	return cli.Report("github_pr_diff", map[string]any{"repo": argv[0], "number": argv[1],
		"base_sha": before.Base.SHA, "head_sha": before.Head.SHA, "diff": string(diff)}, err, 0, stdout, stderr)
}

func githubPages(path string) ([]json.RawMessage, error) {
	all := []json.RawMessage{}
	for page := 1; page <= 100; page++ {
		var batch []json.RawMessage
		if err := githubJSON(fmt.Sprintf("%s?per_page=100&page=%d", path, page), &batch); err != nil {
			return nil, err
		}
		if batch == nil {
			return nil, fmt.Errorf("GitHub returned a missing discussion page")
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			return all, nil
		}
	}
	return nil, fmt.Errorf("discussion exceeds 100 pages; no partial content returned")
}

func githubPRView(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	path, err := githubPRPath(argv)
	var pr map[string]json.RawMessage
	if err == nil {
		err = githubJSON(path, &pr)
	}
	if err == nil && len(pr) == 0 {
		err = fmt.Errorf("GitHub returned missing PR metadata")
	}
	result := map[string]any{"pull_request": pr}
	if err == nil {
		for _, collection := range []struct{ name, path string }{
			{"comments", "repos/" + argv[0] + "/issues/" + argv[1] + "/comments"},
			{"reviews", path + "/reviews"},
			{"review_comments", path + "/comments"},
		} {
			result[collection.name], err = githubPages(collection.path)
			if err != nil {
				break
			}
		}
	}
	return cli.Report("github_pr_view", result, err, 0, stdout, stderr)
}

func githubFile(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	read := func() (any, error) {
		repo, err := githubRepoPath(argv[0])
		if err != nil {
			return nil, err
		}
		if !githubCommit.MatchString(argv[1]) {
			return nil, fmt.Errorf("commit must be a full 40-character SHA")
		}
		parts := strings.Split(argv[2], "/")
		for i, part := range parts {
			if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n") {
				return nil, fmt.Errorf("path must be a repository-relative file path without empty, dot or parent components")
			}
			parts[i] = url.PathEscape(part)
		}
		var file struct {
			Type, Encoding, Content, SHA string
			Size                         *int64
			Submodule                    string `json:"submodule_git_url"`
		}
		if err := githubJSON(repo+"/contents/"+strings.Join(parts, "/")+"?ref="+argv[1], &file); err != nil {
			return nil, err
		}
		if file.Type != "file" || file.Submodule != "" || file.Encoding != "base64" || !githubCommit.MatchString(file.SHA) || file.Size == nil {
			return nil, fmt.Errorf("GitHub did not return a complete file with base64 content and blob identity; large or non-file content is unsupported")
		}
		content, err := base64.StdEncoding.DecodeString(file.Content)
		if err != nil || int64(len(content)) != *file.Size {
			return nil, fmt.Errorf("GitHub returned invalid or incomplete file content")
		}
		if !utf8.Valid(content) || strings.ContainsRune(string(content), '\x00') {
			return nil, fmt.Errorf("file is binary or is not UTF-8 text")
		}
		return map[string]any{"repo": argv[0], "commit": argv[1], "path": argv[2], "sha": file.SHA, "content": string(content)}, nil
	}
	result, err := read()
	return cli.Report("github_file", result, err, 0, stdout, stderr)
}
