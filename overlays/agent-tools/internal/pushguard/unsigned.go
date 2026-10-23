package pushguard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// UnsignedKey, set to true for one push with `git -c`, lets the operator's
// unsigned commits through.
const UnsignedKey = "push-guard.allowUnsigned"

// verifyTimeout bounds CheckUnsigned's git commands. Verifying a signature
// needs only the public key, never a touch, but the verifier can still wait
// on an agent or a keyserver.
var verifyTimeout = 30 * time.Second

// listed is how many of the refused commits a refusal names.
const listed = 20

// CheckUnsigned refuses the push to url when a commit it publishes was
// committed under this checkout's user.email and carries no signature or a
// bad one. remote is the remote as git names it to a pre-push hook: a
// remote's name, or the URL when the push named one. Commits the remote has
// are not published: those the updated refs point at already, and those
// reachable from the remote-tracking refs of remote, or of any remote that
// fetches from or pushes to url. A push to a path on this machine publishes
// nothing.
//
// A commit is signed when it has a gpgsig header: `%G?` alone says N for a
// signed commit whose kind of signature this machine is not set up to
// verify. Of the signed commits only a bad signature (B), which no longer
// matches the commit, refuses; the other statuses (G, U, X, Y, R, E) turn on
// how far this machine trusts the key, which is not what GitHub checks.
func CheckUnsigned(remote, url string, updates []Update) error {
	if url == "" || strings.HasPrefix(url, "file://") || strings.HasPrefix(url, "/") || !strings.Contains(url, ":") {
		return nil
	}
	if allowed, _ := git.Run("config", "--type=bool", "--get", UnsignedKey); allowed == "true" {
		return nil
	}
	me, _ := git.Run("config", "--get", "user.email")
	if me == "" {
		return nil
	}

	var tips, known []string
	for _, u := range updates {
		if !absent(u.Local) && git.OK("cat-file", "-e", u.Local+"^{commit}") {
			tips = append(tips, u.Local)
		}
		if !absent(u.Remote) && git.OK("cat-file", "-e", u.Remote+"^{commit}") {
			known = append(known, u.Remote)
		}
	}
	if len(tips) == 0 {
		return nil
	}
	tracked, err := trackingRefs(remote, url)
	if err != nil {
		return err
	}
	known = append(known, tracked...)

	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
	defer cancel()
	timedOut := func(err error) error {
		if ctx.Err() == nil {
			return err
		}
		return refusal.New("checking the signatures of the commits this push publishes to %s took over %v, so the push is refused: %v\n"+
			"Pushing without the check is the operator's decision; an agent stops and asks them. "+
			"They can push with: git -c %s=true push ...", url, verifyTimeout, err, UnsignedKey)
	}

	args := append(append([]string{"log", "--format=%H%x00%h %s%x00%ce"}, tips...), "--not")
	out, err := git.BytesContext(ctx, nil, append(args, known...)...)
	if err != nil {
		return timedOut(err)
	}
	var mine []string
	names := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Split(line, "\x00")
		if len(fields) == 3 && strings.EqualFold(fields[2], me) {
			mine = append(mine, fields[0])
			names[fields[0]] = fields[1]
		}
	}
	if len(mine) == 0 {
		return nil
	}

	signed, err := withSignature(ctx, mine)
	if err != nil {
		return timedOut(err)
	}
	var refused, check []string
	for _, oid := range mine {
		if signed[oid] {
			check = append(check, oid)
		} else {
			refused = append(refused, names[oid])
		}
	}
	if len(check) > 0 {
		out, err := git.BytesContext(ctx, []byte(strings.Join(check, "\n")+"\n"),
			"log", "--no-walk=unsorted", "--stdin", "--format=%H %G?")
		if err != nil {
			return timedOut(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if oid, status, _ := strings.Cut(line, " "); status == "B" {
				refused = append(refused, names[oid]+" (bad signature)")
			}
		}
	}
	if len(refused) == 0 {
		return nil
	}

	list := refused
	if len(list) > listed {
		list = append(list[:listed:listed], fmt.Sprintf("and %d more", len(refused)-listed))
	}
	return refusal.New("this push publishes commits to %s that were committed as %s and are not signed:\n  %s\n"+
		"Sign them with the signing pass, `git sign-pass`, and push again; an agent hands that to the operator.\n"+
		"To publish them unsigned, for this push only: git -c %s=true push ...",
		url, me, strings.Join(list, "\n  "), UnsignedKey)
}

// trackingRefs are rev-list arguments for the remote-tracking refs of
// remote, or of every remote whose fetch or push URL is url.
func trackingRefs(remote, url string) ([]string, error) {
	names, err := git.Run("remote")
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, name := range strings.Fields(names) {
		match := name == remote
		for _, push := range [][]string{nil, {"--push"}} {
			if match {
				break
			}
			urls, _ := git.Run(append(append([]string{"remote", "get-url", "--all"}, push...), name)...)
			for _, u := range strings.Fields(urls) {
				match = match || u == url
			}
		}
		if match {
			refs = append(refs, "--remotes="+name+"/*")
		}
	}
	return refs, nil
}

// withSignature reports which of the commits carry a signature header.
func withSignature(ctx context.Context, commits []string) (map[string]bool, error) {
	out, err := git.BytesContext(ctx, []byte(strings.Join(commits, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	signed := map[string]bool{}
	for len(out) > 0 {
		header, rest, ok := bytes.Cut(out, []byte("\n"))
		fields := strings.Fields(string(header))
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("unexpected git cat-file output: %q", header)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size+1 > len(rest) {
			return nil, errors.New("truncated git cat-file output")
		}
		head, _, _ := bytes.Cut(rest[:size], []byte("\n\n"))
		for _, line := range strings.Split(string(head), "\n") {
			if strings.HasPrefix(line, "gpgsig ") || strings.HasPrefix(line, "gpgsig-sha256 ") {
				signed[fields[0]] = true
			}
		}
		out = rest[size+1:]
	}
	return signed, nil
}
