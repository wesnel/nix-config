package guard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/claims"
	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
)

// bodyForm is the call that works from wherever a session is.
const bodyForm = "`cd WT && gh pr edit --body-file FILE` (or `cd WT && gh pr create --draft ... --body-file FILE`) as a " +
	"command line of its own, WT being the worktree of the pull request's branch"

const (
	bodyUnseen = "The guard reads a pull request's description for verification claims that need an audit, and cannot read " +
		"this one. Write the description to a file in a command of its own, then run " + bodyForm + "."
	bodyInline = "Verification claims are checked in a description read from a file, and this one is given inline. Write it " +
		"to a file in a command of its own, then run " + bodyForm + "."
	bodyNoRepository = "The description makes verification claims, which are audited for the pull request's change, and " +
		"the guard finds no repository for it where this runs or where the file is. Run " + bodyForm + "."
)

// ghBodyValueFlags are the `gh pr create` and `gh pr edit` flags, other
// than the description's, whose value is the next word.
var ghBodyValueFlags = set("--title", "-t", "--base", "-B", "--head", "-H", "--repo", "-R", "--assignee", "-a",
	"--label", "-l", "--milestone", "-m", "--project", "-p", "--reviewer", "-r", "--recover")

// ghFiles is what the guard knows of where a gh call runs and of the files
// it reads.
type ghFiles struct {
	// workspace is where the call runs when it is its command line's only
	// call but for cds before it, so that nothing else on the line can
	// have written a file it reads; "" otherwise.
	workspace string
	// dir is where the call runs, "" when the guard cannot tell.
	dir string
	// written are the files the line's calls write as far as the guard can
	// tell, where listed is set: for a line read at the top level.
	written []string
	listed  bool
}

// checkBody refuses the description a `gh pr create` or `gh pr edit` call
// sets, args being the words after gh, where it makes verification claims
// an audit does not hold for the pull request's change (checkClaims), or
// where the guard cannot read it: a body the shell expands, one from stdin,
// an editor, a template or the browser, or a file it cannot read. A file
// with claims is read only where nothing else on the command line can have
// written it first (files.workspace); one without passes wherever no call
// the guard reads writes it. A body given inline is refused where it holds
// the claims' heading, since the claims are checked from a file. --fill
// takes the commits' messages, and is let through.
func checkBody(args []string, expanded []bool, files ghFiles) string {
	var positional []string
	head, repo := "", ""
	type claimed struct{ path, body string }
	var bodies []claimed
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, attached := strings.Cut(a, "=")
		if !strings.HasPrefix(a, "--") && len(a) > 2 && (a[:2] == "-b" || a[:2] == "-F") {
			name, value, attached = a[:2], strings.TrimPrefix(a[2:], "="), true
		}
		switch {
		case name == "--template" || name == "-T" || name == "--editor" || name == "-e" || name == "--web" || name == "-w":
			return bodyUnseen
		case ghBodyValueFlags[name]:
			if !attached && i+1 < len(args) {
				i++
				value = args[i]
			}
			switch name {
			case "--head", "-H":
				head = value
			case "--repo", "-R":
				repo = value
			}
			continue
		case !strings.HasPrefix(a, "-"):
			positional = append(positional, a)
			continue
		case name != "--body" && name != "-b" && name != "--body-file" && name != "-F":
			continue
		}
		at := i
		if !attached {
			if i+1 >= len(args) {
				return ""
			}
			i++
			value, at = args[i], i
		}
		switch {
		case expanded[at]:
			return bodyUnseen
		case name == "--body" || name == "-b":
			if _, ok := claims.Section(value); ok {
				return bodyInline
			}
			continue
		case value == "-":
			return bodyUnseen
		}
		path := expandPath(value, files.dir)
		if !filepath.IsAbs(path) {
			return bodyUnseen
		}
		body, err := readBody(path)
		if err != nil {
			return bodyUnseen
		}
		section, ok := claims.Section(string(body))
		hasClaims := ok && len(claims.Parse(section)) > 0
		switch {
		case hasClaims && files.workspace == "":
			return bodyUnseen
		case hasClaims:
			bodies = append(bodies, claimed{path, string(body)})
		case files.workspace == "" && (!files.listed || slices.Contains(files.written, path)):
			return bodyUnseen
		}
	}
	// gh's own words are pr and the verb; a pull request is named after
	// them.
	selector := ""
	if len(positional) > 2 {
		selector = positional[2]
	}
	for _, b := range bodies {
		if reason := checkClaims(b.body, b.path, files.dir, selector, head, repo); reason != "" {
			return reason
		}
	}
	return ""
}

// checkClaims is why body, read from path, may not go out for the pull
// request selector names, or else the one of branch head, or else the one
// of the branch checked out in dir, by claims.Gate for that change, or "".
// The repository is dir's, else path's.
func checkClaims(body, path, dir, selector, head, repo string) string {
	top := ""
	for _, d := range []string{dir, filepath.Dir(path)} {
		if d == "" {
			continue
		}
		if t, err := git.Run("-C", d, "rev-parse", "--show-toplevel"); err == nil && t != "" {
			top = t
			break
		}
	}
	if top == "" {
		return bodyNoRepository
	}
	var tip string
	var err error
	switch {
	case selector != "":
		tip, err = pullRequestTip(top, selector, repo)
	case head != "":
		_, branch, owned := strings.Cut(head, ":")
		if !owned {
			branch = head
		}
		tip, err = branchTip(top, branch)
	default:
		tip, err = git.Run("-C", top, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	}
	if err != nil {
		return fmt.Sprintf("The description makes verification claims, which are audited for the pull request's change, "+
			"and the guard cannot tell which change that is in %s: %v. Run %s.", top, err, bodyForm)
	}
	if err := claims.Gate(top, tip, body, false); err != nil {
		reason := err.Error()
		return strings.ToUpper(reason[:1]) + reason[1:] + "."
	}
	return ""
}

// pullRequestURL is a pull request's URL on GitHub.
var pullRequestURL = regexp.MustCompile(`^https://github\.com/([^/]+/[^/]+)/pull/([0-9]+)/?$`)

// pullRequestTip is the tip, in the repository at top, of the local branch
// of the pull request selector names, as gh takes it: by number, URL or
// head branch. GitHub names a numbered pull request's branch, through gh,
// in the repository repo names, else in the one top's remotes name.
func pullRequestTip(top, selector, repo string) (string, error) {
	number, slug := strings.TrimPrefix(selector, "#"), repo
	if m := pullRequestURL.FindStringSubmatch(selector); m != nil {
		number, slug = m[2], m[1]
	}
	if number == "" || strings.Trim(number, "0123456789") != "" {
		return branchTip(top, selector)
	}
	if parts := strings.Split(slug, "/"); len(parts) == 3 {
		slug = parts[1] + "/" + parts[2]
	}
	if slug == "" {
		slug = remoteSlug(top)
	}
	if slug == "" {
		return "", errors.New("no remote of it is on GitHub, and no --repo is given")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", number, "--repo", slug, "--json", "headRefName")
	// Outside any repository, where gh runs none of a repository's hooks.
	cmd.Dir = gh.Outside
	data, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("`gh pr view %s --repo %s` failed: %w", number, slug, err)
	}
	var pr struct {
		HeadRefName string `json:"headRefName"`
	}
	if json.Unmarshal(data, &pr) != nil || pr.HeadRefName == "" {
		return "", fmt.Errorf("`gh pr view %s --repo %s` names no branch", number, slug)
	}
	return branchTip(top, pr.HeadRefName)
}

// branchTip is the commit at the tip of local branch name in the
// repository at top.
func branchTip(top, name string) (string, error) {
	tip, err := git.Run("-C", top, "rev-parse", "--verify", "-q", "--end-of-options", "refs/heads/"+name+"^{commit}")
	if err != nil || tip == "" {
		return "", fmt.Errorf("no local branch %s", name)
	}
	return tip, nil
}

// remoteSlug is the GitHub repository whose pull requests gh works on from
// the repository at top: the remote `gh repo set-default` marks, else
// upstream, github or origin, else the only remote on GitHub; "" for none.
func remoteSlug(top string) string {
	out, err := git.Run("-C", top, "remote", "-v")
	if err != nil {
		return ""
	}
	slugs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 3 && f[2] == "(fetch)" && gh.Slug(f[1]) != "" {
			slugs[f[0]] = gh.Slug(f[1])
		}
	}
	resolved, _ := git.Run("-C", top, "config", "--get-regexp", `^remote\..*\.gh-resolved$`)
	for _, line := range strings.Split(resolved, "\n") {
		key, value, _ := strings.Cut(line, " ")
		remote := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".gh-resolved")
		if value == "base" && slugs[remote] != "" {
			return slugs[remote]
		}
	}
	for _, remote := range []string{"upstream", "github", "origin"} {
		if slugs[remote] != "" {
			return slugs[remote]
		}
	}
	if len(slugs) == 1 {
		for _, slug := range slugs {
			return slug
		}
	}
	return ""
}

// readBody reads a regular file of at most a mebibyte, through any
// symlinks.
func readBody(path string) ([]byte, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("not a small regular file")
	}
	return os.ReadFile(real)
}
