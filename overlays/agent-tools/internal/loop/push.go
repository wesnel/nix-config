package loop

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/history"
	"github.com/wesnel/agent-tools/internal/plans"
)

// Push uploads the local commits of branches (default: HEAD's) through the
// Git Database API and moves them with one updateRefs call each, bottom up.
// Each branch's expected head on GitHub is its lease, else its remote
// tracking ref: GitHub's head must still be that commit, and updateRefs
// moves the branch only from it, so a push made elsewhere is never
// overwritten. Without a lease the local branch must contain it; a lease
// is how a rebased branch is pushed. On a ready PR every commit the push
// adds must be a fixup of the PR's own; a draft takes any commit. Each
// object GitHub creates is compared with the local SHA before any branch
// moves.
func Push(branches []string, leases map[string]string, dryRun bool) (any, error) {
	if os.Getenv(ModeEnv) == "people" {
		return nil, refuse("in a people-mode review loop the operator pushes, after reading the fixups, with " +
			"`git sign-pass --push`. Only the operator starts a loop, with `claude-review-loop --bots|--people`; " +
			"commit the fixups and say in the report that they wait for that push")
	}
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	prs, err := openPRs(repo)
	if err != nil {
		return nil, err
	}
	if len(branches) == 0 {
		current, err := currentBranch()
		if err != nil {
			return nil, err
		}
		branches = []string{current}
	}
	for b := range leases {
		if !slices.Contains(branches, b) {
			return nil, refuse("--lease names %s, which is not pushed here; name the branch too, or drop its lease", b)
		}
	}
	for _, b := range branches {
		if _, err := checkBranch(repo, prs, b); err != nil {
			return nil, err
		}
	}
	order := stackOrder(prs, branches[0])
	for _, b := range branches {
		if !slices.Contains(order, b) {
			return nil, refuse("%s is not in the stack of %s, and one push moves one stack, bottom up; push it on its own", b, branches[0])
		}
	}
	slices.SortStableFunc(branches, func(a, b string) int { return slices.Index(order, a) - slices.Index(order, b) })

	type update struct{ branch, after, before string }
	var updates []update
	known := []string{}
	for _, b := range branches {
		pr := prs[b]
		expected, leased := leases[b]
		if leased {
			if expected, err = fullOid(expected); err != nil {
				return nil, err
			}
		} else if expected, err = remoteHead(repo, b); err != nil {
			return nil, err
		}
		if pr.HeadRefOid != expected {
			return nil, refuse("%s on GitHub is at %.12s, not %.12s as expected, so someone else pushed; fetch it, look at what "+
				"changed, then push again with `--lease %s=%s` if it is still to be replaced", b, pr.HeadRefOid, expected, b, pr.HeadRefOid)
		}
		local, err := git.Run("rev-parse", "--verify", "-q", "refs/heads/"+b)
		if err != nil {
			return nil, refuse("no local branch %s; check it out, or leave it out of the push", b)
		}
		if !leased && !git.OK("merge-base", "--is-ancestor", expected, local) {
			return nil, refuse("local %s does not contain %.12s, its head on GitHub, so pushing would drop commits; if it was "+
				"rebased on purpose, push it with `--lease %s=%s`", b, expected, b, expected)
		}
		remoteParent, localParent, err := parents(repo, prs, pr.BaseRefName)
		if err != nil {
			return nil, err
		}
		if !pr.IsDraft {
			before, err := commitsBetween(remoteParent, expected)
			if err != nil {
				return nil, err
			}
			now, err := commitsBetween(localParent, local)
			if err != nil {
				return nil, err
			}
			if err := checkFixups(b, own(before), now); err != nil {
				return nil, err
			}
			if err := checkPatches(b, remoteParent, expected, localParent, local); err != nil {
				return nil, err
			}
		}
		known = append(known, expected)
		if git.OK("cat-file", "-e", remoteParent+"^{commit}") {
			known = append(known, remoteParent)
		}
		if local != expected {
			updates = append(updates, update{b, local, expected})
		}
	}
	if len(updates) == 0 {
		return map[string]any{"pushed": map[string]string{}}, nil
	}

	tips := make([]string, len(updates))
	for i, u := range updates {
		tips[i] = u.after
	}
	objects, err := missingObjects(tips, known)
	if err != nil {
		return nil, err
	}

	if dryRun {
		would := map[string]string{}
		for _, u := range updates {
			would[u.branch] = u.after
		}
		return map[string]any{"wouldUpdate": would, "objects": objects.counts()}, nil
	}

	calls, err := objects.upload(repo.Slug)
	if err != nil {
		return nil, err
	}

	// One branch per call, bottom up: GitHub starts a pull request's
	// workflows twice when one push moves it and its base together. Each
	// branch moved updates its tracking ref at once, so a call that fails
	// leaves the next push to carry on from it.
	pushed := map[string]string{}
	for _, u := range updates {
		err = graphQL(`mutation($input: UpdateRefsInput!) { updateRefs(input: $input) { clientMutationId } }`,
			map[string]any{"input": map[string]any{"repositoryId": repo.ID, "refUpdates": []map[string]any{
				{"name": "refs/heads/" + u.branch, "afterOid": u.after, "beforeOid": u.before, "force": true}}}}, nil)
		if err != nil {
			if len(pushed) > 0 {
				err = fmt.Errorf("%w; GitHub has %s, so run `pr-loop push` again to push the rest", err, branchTips(branches, pushed))
			}
			return nil, err
		}
		if err := recordTracking(repo, u.branch, u.after); err != nil {
			return nil, err
		}
		pushed[u.branch] = u.after
	}
	logEvent(plans.Event{Kind: "pushed", Summary: "pushed " + branchTips(branches, pushed)})
	return map[string]any{"pushed": pushed, "apiCalls": calls}, nil
}

// remoteHead is branch's head as this checkout last saw it on GitHub: its
// remote tracking ref, origin's first.
func remoteHead(repo *Repo, branch string) (string, error) {
	remotes, err := githubRemotes()
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(remotes))
	for name, slug := range remotes {
		if slug == repo.Slug {
			names = append(names, name)
		}
	}
	slices.SortFunc(names, func(a, b string) int {
		switch {
		case a == b:
			return 0
		case a == "origin":
			return -1
		case b == "origin":
			return 1
		}
		return strings.Compare(a, b)
	})
	for _, remote := range names {
		if oid, err := git.Run("rev-parse", "--verify", "-q", "refs/remotes/"+remote+"/"+branch); err == nil {
			return oid, nil
		}
	}
	return "", refuse("no remote tracking ref for %s, so what it replaces on GitHub is unknown; pass the head it "+
		"replaces, as `pr-loop stack` shows it, with `--lease %s=SHA`", branch, branch)
}

// parents is what a PR on base is measured against: on GitHub, the head of
// the PR base heads, else base's tracking ref; and here, that PR's local
// branch, else the same tracking ref.
func parents(repo *Repo, prs map[string]*PR, base string) (remote, local string, err error) {
	if below := prs[base]; below != nil {
		local = below.HeadRefOid
		if oid, err := git.Run("rev-parse", "--verify", "-q", "refs/heads/"+base); err == nil {
			local = oid
		}
		return below.HeadRefOid, local, nil
	}
	ref, err := trackingRef(repo, base)
	return ref, ref, err
}

// own are a branch's own commits among commits, its fixups left out.
func own(commits []Commit) []Commit {
	var out []Commit
	for _, c := range commits {
		if kind, _ := fold(c.Message); kind == history.Kept {
			out = append(out, c)
		}
	}
	return out
}

// objectSet is what GitHub needs before a branch can point at a tip: every
// object reachable from the tips and from none of the commits it has.
type objectSet struct {
	kinds   map[string]string
	order   []string
	commits []string // parents first
}

func missingObjects(tips, known []string) (*objectSet, error) {
	exclude := append([]string{"--not"}, known...)
	listed, err := git.Run(append(append([]string{"rev-list", "--objects"}, tips...), exclude...)...)
	if err != nil {
		return nil, err
	}
	var shas []string
	for _, line := range strings.Split(listed, "\n") {
		if sha, _, _ := strings.Cut(line, " "); sha != "" {
			shas = append(shas, sha)
		}
	}
	set := &objectSet{kinds: map[string]string{}}
	if len(shas) > 0 {
		out, err := git.Bytes([]byte(strings.Join(shas, "\n")+"\n"), "cat-file", "--batch-check=%(objectname) %(objecttype)")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			sha, kind, _ := strings.Cut(line, " ")
			set.kinds[sha] = kind
			set.order = append(set.order, sha)
		}
	}
	commits, err := git.Run(append(append([]string{"rev-list", "--topo-order", "--reverse"}, tips...), exclude...)...)
	if err != nil {
		return nil, err
	}
	set.commits = strings.Fields(commits)
	return set, nil
}

func (s *objectSet) counts() map[string]int {
	counts := map[string]int{}
	for _, k := range s.kinds {
		counts[k]++
	}
	return counts
}

// upload creates the objects through the Git Database API, children before
// the trees and commits that name them, and returns the number of API calls.
func (s *objectSet) upload(slug string) (int, error) {
	up := &uploader{slug: slug, missingTrees: map[string]bool{}, done: map[string]bool{}}
	for sha, kind := range s.kinds {
		if kind == "tree" {
			up.missingTrees[sha] = true
		}
	}
	for _, sha := range s.order {
		if s.kinds[sha] == "blob" {
			if err := up.blob(sha); err != nil {
				return up.calls, err
			}
		}
	}
	for _, sha := range s.order {
		if s.kinds[sha] == "tree" && !up.done[sha] {
			if err := up.tree(sha); err != nil {
				return up.calls, err
			}
		}
	}
	for _, sha := range s.commits {
		if err := up.commit(sha); err != nil {
			return up.calls, err
		}
	}
	return up.calls, nil
}

type uploader struct {
	slug         string
	missingTrees map[string]bool
	done         map[string]bool
	calls        int
}

func (u *uploader) post(kind string, body any) (string, error) {
	u.calls++
	var reply struct{ SHA string }
	if err := objectsREST("POST", fmt.Sprintf("repos/%s/git/%s", u.slug, kind), body, &reply); err != nil {
		return "", err
	}
	return reply.SHA, nil
}

func (u *uploader) expect(kind, want, got string) error {
	if want != got {
		return refuse("GitHub created %s %s for local %s, so nothing was pushed; hand this push to the operator", kind, got, want)
	}
	u.done[want] = true
	return nil
}

func (u *uploader) blob(sha string) error {
	content, err := git.Bytes(nil, "cat-file", "blob", sha)
	if err != nil {
		return err
	}
	// The API stores the pointer but never uploads what it points at.
	if bytes.HasPrefix(content, []byte("version https://git-lfs.github.com/spec/")) {
		return refuse("blob %s is a Git LFS pointer, whose object the GitHub API cannot upload; hand this push to the operator", sha)
	}
	got, err := u.post("blobs", map[string]string{"content": base64.StdEncoding.EncodeToString(content), "encoding": "base64"})
	if err != nil {
		return err
	}
	return u.expect("blob", sha, got)
}

func (u *uploader) tree(sha string) error {
	out, err := git.Bytes(nil, "ls-tree", "-z", sha)
	if err != nil {
		return err
	}
	var entries []map[string]string
	for _, item := range bytes.Split(out, []byte{0}) {
		if len(item) == 0 {
			continue
		}
		meta, name, _ := bytes.Cut(item, []byte{'\t'})
		fields := strings.Fields(string(meta))
		mode, kind, child := fields[0], fields[1], fields[2]
		if kind == "tree" && u.missingTrees[child] && !u.done[child] {
			if err := u.tree(child); err != nil {
				return err
			}
		}
		entries = append(entries, map[string]string{"path": string(name), "mode": mode, "type": kind, "sha": child})
	}
	if len(entries) == 0 {
		return refuse("tree %s is empty, which the API cannot create; hand this push to the operator", sha)
	}
	got, err := u.post("trees", map[string]any{"tree": entries})
	if err != nil {
		return err
	}
	return u.expect("tree", sha, got)
}

var commitHeaders = map[string]bool{"tree": true, "parent": true, "author": true, "committer": true, "gpgsig": true}

// commitBody turns a raw commit object into a create-commit request that
// hashes to the same SHA, or refuses if the API cannot express it.
func commitBody(sha string, raw []byte) (map[string]any, error) {
	header, message, _ := bytes.Cut(raw, []byte("\n\n"))
	fields := map[string]string{}
	var parents, signature []string
	current := ""
	for _, line := range strings.Split(string(header), "\n") {
		if strings.HasPrefix(line, " ") && current == "gpgsig" {
			signature = append(signature, line[1:])
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		current = key
		if !commitHeaders[key] {
			return nil, refuse("commit %s has a %s header, which the API cannot reproduce; hand this push to the operator", sha, key)
		}
		switch key {
		case "parent":
			parents = append(parents, value)
		case "gpgsig":
			signature = append(signature, value)
		default:
			fields[key] = value
		}
	}
	if !utf8.Valid(message) {
		return nil, refuse("commit %s has a message that is not UTF-8, which the API cannot reproduce; hand this push to the operator", sha)
	}
	author, err := identity(fields["author"])
	if err != nil {
		return nil, err
	}
	committer, err := identity(fields["committer"])
	if err != nil {
		return nil, err
	}
	body := map[string]any{"message": string(message), "tree": fields["tree"], "parents": parents, "author": author, "committer": committer}
	if parents == nil {
		body["parents"] = []string{}
	}
	if len(signature) > 0 {
		body["signature"] = strings.Join(signature, "\n")
	}
	return body, nil
}

func (u *uploader) commit(sha string) error {
	raw, err := git.Bytes(nil, "cat-file", "commit", sha)
	if err != nil {
		return err
	}
	body, err := commitBody(sha, raw)
	if err != nil {
		return err
	}
	got, err := u.post("commits", body)
	if err != nil {
		return err
	}
	return u.expect("commit", sha, got)
}

// identity turns "Name <email> 1790750341 -0700" into the API's form, keeping
// the original offset so the commit hashes the same.
func identity(value string) (map[string]string, error) {
	fields := strings.Fields(value)
	if len(fields) < 3 {
		return nil, fmt.Errorf("malformed identity %q", value)
	}
	tz := fields[len(fields)-1]
	epoch, err := strconv.ParseInt(fields[len(fields)-2], 10, 64)
	if err != nil || len(tz) != 5 {
		return nil, fmt.Errorf("malformed identity %q", value)
	}
	nameEmail := strings.TrimSuffix(value, " "+fields[len(fields)-2]+" "+tz)
	name, email, ok := strings.Cut(strings.TrimSuffix(nameEmail, ">"), " <")
	if !ok {
		return nil, fmt.Errorf("malformed identity %q", value)
	}
	hours, _ := strconv.Atoi(tz[1:3])
	minutes, _ := strconv.Atoi(tz[3:5])
	offset := hours*3600 + minutes*60
	if tz[0] == '-' {
		offset = -offset
	}
	date := time.Unix(epoch, 0).In(time.FixedZone("", offset)).Format("2006-01-02T15:04:05-07:00")
	return map[string]string{"name": name, "email": email, "date": date}, nil
}
