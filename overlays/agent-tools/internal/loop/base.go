package loop

import (
	"sort"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
)

// baseURL is where FetchBase fetches a repository's branches from; tests
// replace it.
var baseURL = func(slug string) string { return "https://github.com/" + slug + ".git" }

// credentialHelper lets the fetch alone use the gh login, for a private
// repository, through the environment, so it is in no argv and in no config
// git reads for any other command. HTTPS needs no key touch, as SSH would.
var credentialHelper = []string{
	"GIT_CONFIG_COUNT=1",
	"GIT_CONFIG_KEY_0=credential.https://github.com.helper",
	"GIT_CONFIG_VALUE_0=!gh auth git-credential",
}

// FetchBase fetches the branch each of the loop's PRs not stacked on
// another is based on, as GitHub has it now, into the remote-tracking refs
// of this repository's GitHub remotes, where a rebase in the PR's worktree
// and the push's checks find it, and records its tip as the base the loop
// knows. It writes nothing to GitHub.
func FetchBase() (any, error) {
	state, repo, prs, err := context(nil)
	if err != nil {
		return nil, err
	}
	remotes, err := githubRemotes()
	if err != nil {
		return nil, err
	}
	var names []string
	for name, slug := range remotes {
		if slug == repo.Slug {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, refuse("no remote of this checkout fetches %s; ask the operator to add one", repo.Slug)
	}
	fetched := map[string]map[string]string{}
	for _, b := range state.Order {
		entry := state.Branches[b]
		if _, stacked := state.Branches[entry.Base]; stacked {
			continue
		}
		pr := prs[b]
		if pr.BaseRefName != entry.Base {
			return nil, refuse("#%d is based on %s on GitHub, not %s as when the loop began; stop and ask the operator",
				pr.Number, pr.BaseRefName, entry.Base)
		}
		base := pr.BaseRefName
		if strings.HasPrefix(base, "-") || strings.ContainsAny(base, ": \t\n*?[\\^~") {
			return nil, refuse("GitHub gave %q as the base of #%d, which pr-loop does not fetch; ask the operator", base, pr.Number)
		}
		target := "refs/remotes/" + names[0] + "/" + base
		if _, ok := fetched[base]; !ok {
			if _, err := git.RunEnv(credentialHelper, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules",
				"--no-write-fetch-head", baseURL(repo.Slug), "+refs/heads/"+base+":"+target); err != nil {
				return nil, err
			}
			got, err := git.Run("rev-parse", "--verify", target+"^{commit}")
			if err != nil {
				return nil, err
			}
			if got != pr.BaseRefOid {
				return nil, refuse("GitHub reports %s at %.12s, but the fetch got %.12s, so it moved; run `pr-loop fetch-base` again",
					base, pr.BaseRefOid, got)
			}
			for _, other := range names[1:] {
				if _, err := git.Run("update-ref", "refs/remotes/"+other+"/"+base, got); err != nil {
					return nil, err
				}
			}
			fetched[base] = map[string]string{"ref": target, "oid": got}
		}
		if entry.BaseOid != pr.BaseRefOid {
			fetched[base]["was"] = entry.BaseOid
		}
		entry.BaseOid = pr.BaseRefOid
	}
	return map[string]any{"fetched": fetched}, state.save()
}
