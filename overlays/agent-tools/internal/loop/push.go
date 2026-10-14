package loop

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
)

// Push uploads the local commits through the Git Database API and moves the
// branches with one atomic updateRefs call. Each object GitHub creates is
// compared with the local SHA before any branch moves, and each branch move is
// guarded by the commit the loop last saw on GitHub.
func Push(branches []string, dryRun bool) (any, error) {
	state, repo, prs, err := context(nil)
	if err != nil {
		return nil, err
	}
	if len(branches) == 0 {
		branches = state.Order
	}
	type update struct{ branch, after, before string }
	var updates []update
	for _, b := range branches {
		pr, ok := prs[b]
		if !ok {
			return nil, refuse("%s was not registered with `pr-loop begin`; push only the loop's branches, which `pr-loop status` lists", b)
		}
		entry := state.Branches[b]
		if pr.HeadRefOid != entry.RemoteOid {
			return nil, refuse("%s on GitHub moved to %.12s since the loop last pushed %.12s, so someone else pushed; stop and ask Wesley",
				b, pr.HeadRefOid, entry.RemoteOid)
		}
		parent, err := state.parentRef(repo, b)
		if err != nil {
			return nil, err
		}
		now, err := commitsBetween(parent, "refs/heads/"+b)
		if err != nil {
			return nil, err
		}
		if err := checkFixups(b, entry.Commits, now); err != nil {
			return nil, err
		}
		local, err := git.Run("rev-parse", "refs/heads/"+b)
		if err != nil {
			return nil, err
		}
		if local != entry.RemoteOid {
			updates = append(updates, update{b, local, entry.RemoteOid})
		}
	}
	if len(updates) == 0 {
		return map[string]any{"pushed": map[string]string{}}, nil
	}

	known := []string{}
	for _, e := range state.Branches {
		known = append(known, e.RemoteOid)
	}
	for _, b := range state.Order {
		if _, stacked := state.Branches[state.Branches[b].Base]; !stacked {
			parent, err := state.parentRef(repo, b)
			if err != nil {
				return nil, err
			}
			oid, err := git.Run("rev-parse", parent)
			if err != nil {
				return nil, err
			}
			known = append(known, oid)
		}
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

	refUpdates := make([]map[string]any, len(updates))
	for i, u := range updates {
		refUpdates[i] = map[string]any{"name": "refs/heads/" + u.branch, "afterOid": u.after, "beforeOid": u.before, "force": true}
	}
	err = gh.GraphQL(`mutation($input: UpdateRefsInput!) { updateRefs(input: $input) { clientMutationId } }`,
		map[string]any{"input": map[string]any{"repositoryId": repo.ID, "refUpdates": refUpdates}}, nil)
	if err != nil {
		return nil, err
	}
	pushed := map[string]string{}
	for _, u := range updates {
		state.Branches[u.branch].RemoteOid = u.after
		if err := recordTracking(repo, u.branch, u.after); err != nil {
			return nil, err
		}
		pushed[u.branch] = u.after
	}
	state.Round++
	if err := snapshotRound(state); err != nil {
		return nil, err
	}
	return map[string]any{"pushed": pushed, "round": state.Round, "apiCalls": calls}, state.save()
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
	if err := gh.REST("POST", fmt.Sprintf("repos/%s/git/%s", u.slug, kind), body, &reply); err != nil {
		return "", err
	}
	return reply.SHA, nil
}

func (u *uploader) expect(kind, want, got string) error {
	if want != got {
		return refuse("GitHub created %s %s for local %s, so nothing was pushed; hand this push to Wesley", kind, got, want)
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
		return refuse("blob %s is a Git LFS pointer, whose object the GitHub API cannot upload; hand this push to Wesley", sha)
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
		return refuse("tree %s is empty, which the API cannot create; hand this push to Wesley", sha)
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
			return nil, refuse("commit %s has a %s header, which the API cannot reproduce; hand this push to Wesley", sha, key)
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
		return nil, refuse("commit %s has a message that is not UTF-8, which the API cannot reproduce; hand this push to Wesley", sha)
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
