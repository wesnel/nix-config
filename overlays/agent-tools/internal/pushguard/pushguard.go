// Package pushguard refuses a push that would publish a private GitHub
// repository's history in a public one. It sees only history: private code
// copied into a public repository as new commits passes.
package pushguard

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// AllowKey names a public repository, as owner/name, that may receive
// private history. Set it for one push with `git -c`.
const AllowKey = "push-guard.allowPublic"

// unchecked ends a refusal from a check that could not run, taking AllowKey
// and the target.
const unchecked = "Pushing without the check is Wesley's decision; an agent stops and asks him. " +
	"He can push with: git -c %s=%s push ..."

// Update is one line of pre-push's standard input.
type Update struct {
	LocalRef, Local, RemoteRef, Remote string
}

// Parse reads the updates git gives a pre-push hook.
func Parse(r io.Reader) ([]Update, error) {
	var updates []Update
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 4 {
			return nil, fmt.Errorf("unexpected pre-push input: %q", scanner.Text())
		}
		updates = append(updates, Update{fields[0], fields[1], fields[2], fields[3]})
	}
	return updates, scanner.Err()
}

// Lookup reports whether a GitHub repository, as owner/name, is public.
type Lookup func(slug string) (bool, error)

// GitHub looks a repository up with the user's gh login. A repository GitHub
// will not show is not public: every public repository is visible to a
// signed-in user.
func GitHub() Lookup {
	cache := map[string]bool{}
	return func(slug string) (bool, error) {
		if public, ok := cache[slug]; ok {
			return public, nil
		}
		var repo struct {
			Private bool `json:"private"`
		}
		err := gh.REST("GET", "repos/"+slug, nil, &repo)
		var status *gh.StatusError
		switch {
		case errors.As(err, &status) && status.Code == 404:
			repo.Private = true
		case err != nil:
			return false, err
		}
		cache[slug] = !repo.Private
		return !repo.Private, nil
	}
}

func absent(oid string) bool { return strings.Trim(oid, "0") == "" }

// Check refuses the push to url when a commit it sends to a public GitHub
// repository is reachable from a remote-tracking ref of a private one.
// Commits the target already has, or that any public remote has, are not
// counted, so a private fork can still push upstream's own history.
func Check(url string, updates []Update, public Lookup) error {
	target := gh.Slug(url)
	if target == "" {
		return nil
	}
	allowed, _ := git.Run("config", "--get-all", AllowKey)
	for _, a := range strings.Fields(allowed) {
		if strings.EqualFold(a, target) {
			return nil
		}
	}
	isPublic, err := public(target)
	if err != nil {
		return refusal.New("cannot tell whether %s is public, so the push is refused: %v\n"+
			"If `gh auth status` shows no login, hand Wesley `gh auth login`.\n"+unchecked,
			target, err, AllowKey, target)
	}
	if !isPublic {
		return nil
	}

	var tips, known []string
	for _, u := range updates {
		if !absent(u.Local) {
			tips = append(tips, u.Local)
		}
		if !absent(u.Remote) && git.OK("cat-file", "-e", u.Remote+"^{commit}") {
			known = append(known, u.Remote)
		}
	}
	if len(tips) == 0 {
		return nil
	}

	names, err := git.Run("remote")
	if err != nil {
		return err
	}
	var private []string
	slugs := map[string]string{}
	for _, name := range strings.Fields(names) {
		fetch, _ := git.Run("remote", "get-url", name)
		slug := gh.Slug(fetch)
		if slug == "" {
			continue
		}
		slugs[name] = slug
		pub := strings.EqualFold(slug, target)
		if !pub {
			if pub, err = public(slug); err != nil {
				return refusal.New("cannot tell whether %s (remote %s) is private, so the push to %s is refused: %v\n"+
					"If `gh auth status` shows no login, hand Wesley `gh auth login`.\n"+unchecked,
					slug, name, target, err, AllowKey, target)
			}
		}
		if pub {
			known = append(known, "--remotes="+name+"/*")
		} else {
			private = append(private, name)
		}
	}
	sort.Strings(private)

	sending, err := revList(tips, known)
	if err != nil {
		return err
	}
	for _, name := range private {
		kept, err := revList(tips, append(append([]string{}, known...), "--remotes="+name+"/*"))
		if err != nil {
			return err
		}
		keep := map[string]bool{}
		for _, oid := range kept {
			keep[oid] = true
		}
		var leaked []string
		for _, oid := range sending {
			if !keep[oid] {
				leaked = append(leaked, oid)
			}
		}
		if len(leaked) > 0 {
			example, _ := git.Run("log", "-1", "--format=%h %s", leaked[0])
			return refusal.New("%d of the commits this push sends to public %s are history of private %s (remote %s), such as %s.\n"+
				"Publishing private history is Wesley's decision; an agent must stop here and ask.\n"+
				"To publish it anyway: git -c %s=%s push ...",
				len(leaked), target, slugs[name], name, example, AllowKey, target)
		}
	}
	return nil
}

// revList lists the commits reachable from tips and from none of known.
func revList(tips, known []string) ([]string, error) {
	args := append(append([]string{"rev-list"}, tips...), "--not")
	out, err := git.Run(append(args, known...)...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}
