package plans

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// SyncOptions say what a sync does.
type SyncOptions struct {
	// PullOnly takes the other hosts' plans and sends nothing.
	PullOnly bool
	// Settle is how long to wait first, for a burst of writes to end.
	Settle time.Duration
}

// Synced is what a sync did.
type Synced struct {
	Repo string `json:"repo,omitempty"`
	// Skipped says why nothing was done.
	Skipped string `json:"skipped,omitempty"`
	// Committed are this host's files the sync committed.
	Committed []string `json:"committed,omitempty"`
	Pushed    bool     `json:"pushed"`
	// Behind says why the checked-out branch was left behind origin's, so
	// that the other hosts' latest plans are not in the plans directory.
	Behind string `json:"behind,omitempty"`
}

const (
	// gitTimeout bounds each git command that reaches the remote.
	gitTimeout = 60 * time.Second
	// pullTimeout bounds the quiet pull before find, status and new.
	pullTimeout = 10 * time.Second
)

// repo is the plans directory's repository; "" when it is in none.
func (c Config) repo() string {
	if c.Repo != "" {
		return c.Repo
	}
	return filepath.Dir(c.Dir)
}

// git runs git in the notes repository, with the repository unable to run
// programs of its own, and the remote reached through SSHConfig's ssh.
func (c Config) git(ctx context.Context, args ...string) (string, error) {
	return c.gitAs(ctx, nil, args...)
}

// syncName is the name sync commits under.
const syncName = "agent-plan sync"

// syncEmail is the address sync commits under on this host: not the
// operator's user.email, so git-push-guard, which refuses only the unsigned
// commits committed under user.email, lets sync's unsigned commits through.
// .invalid is reserved, so the address is nobody's mailbox.
func (c Config) syncEmail() string { return "agent-plan@" + c.Host + ".invalid" }

// asSync is the environment for a commit whose committer is sync and whose
// author is the operator's user.name and user.email as git resolves them in
// the notes repository, so that GitHub links the commit to their account by
// the author's email. Where git resolves no user.email or no user.name, the
// author is sync too, so a sync never fails for want of one. It goes in the
// environment, not `-c user.email`, which git hands to a push's pre-push hook
// and would change whose commits git-push-guard checks.
func (c Config) asSync(ctx context.Context) []string {
	name, email := syncName, c.syncEmail()
	if n, err := c.git(ctx, "config", "--get", "user.name"); err == nil && n != "" {
		if e, err := c.git(ctx, "config", "--get", "user.email"); err == nil && e != "" {
			name, email = n, e
		}
	}
	return []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + syncName, "GIT_COMMITTER_EMAIL=" + c.syncEmail()}
}

// gitAs is git with env added to its environment.
func (c Config) gitAs(ctx context.Context, env []string, args ...string) (string, error) {
	out, err := c.gitRawAs(ctx, env, args...)
	return strings.TrimSpace(out), err
}

// gitRaw is git with its output as git wrote it, for output whose spaces
// matter, such as file names.
func (c Config) gitRaw(ctx context.Context, args ...string) (string, error) {
	return c.gitRawAs(ctx, nil, args...)
}

func (c Config) gitRawAs(ctx context.Context, env []string, args ...string) (string, error) {
	pre := []string{"-C", c.repo(), "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}
	if c.SSHConfig != "" {
		pre = append(pre, "-c", "core.sshCommand="+c.sshCommand())
	}
	cmd := exec.CommandContext(ctx, "git", append(pre, args...)...)
	cmd.Env = append(append(gitenv.Without(os.Environ()), "GIT_TERMINAL_PROMPT=0"), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func (c Config) sshCommand() string { return "ssh -F " + c.SSHConfig }

// inRepo reports whether the plans directory's repository exists, with its
// top level where the config says.
func (c Config) inRepo() bool {
	top, err := c.git(context.Background(), "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	a, err1 := filepath.EvalSymlinks(top)
	b, err2 := filepath.EvalSymlinks(c.repo())
	return err1 == nil && err2 == nil && a == b
}

// lockSync takes the lock every sync and pull holds, waiting for it unless
// try; it returns nil when try finds it held.
func (c Config) lockSync(try bool) (*os.File, error) {
	return lockFile(filepath.Join(c.Dir, ".sync.lock"), try)
}

// lockFile locks a persistent inode, waiting unless try. Closing the file
// releases the lock; keeping its name lets concurrent callers share it.
func lockFile(path string, try bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644) //nolint:gosec // an empty lock file
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if try {
		how |= syscall.LOCK_NB
	}
	for {
		err = syscall.Flock(int(f.Fd()), how)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		f.Close()
		if try && errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}

// ownFiles are this host's plan files, as paths relative to the
// repository: those in the plans directory, and those in any of trees that
// are gone from it.
func (c Config) ownFiles(ctx context.Context, trees ...string) ([]string, error) {
	rel, err := filepath.Rel(c.repo(), c.Dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if c.ownName(name) && !seen[name] {
			seen[name] = true
			out = append(out, filepath.Join(rel, name))
		}
	}
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			add(e.Name())
		}
	}
	for _, tree := range trees {
		tracked, err := c.git(ctx, "ls-tree", "--name-only", tree, "--", rel+"/")
		if err != nil {
			return nil, err
		}
		for _, f := range strings.Split(tracked, "\n") {
			if f != "" && filepath.Dir(f) == rel {
				add(filepath.Base(f))
			}
		}
	}
	return out, nil
}

// ownName reports whether a file named name in the plans directory is one
// of this host's plan files.
func (c Config) ownName(name string) bool {
	m := planFile.FindStringSubmatch(name)
	return m != nil && m[2] == c.Host
}

// treeBlobs maps each of files that tree holds to its blob.
func (c Config) treeBlobs(ctx context.Context, tree string, files []string) (map[string]string, error) {
	out, err := c.git(ctx, append([]string{"ls-tree", "-z", tree, "--"}, files...)...)
	if err != nil {
		return nil, err
	}
	blobs := map[string]string{}
	for _, entry := range strings.Split(out, "\x00") {
		// mode SP type SP object TAB path
		if meta, path, ok := strings.Cut(entry, "\t"); ok {
			if f := strings.Fields(meta); len(f) == 3 {
				blobs[path] = f[2]
			}
		}
	}
	return blobs, nil
}

// dirBlobs maps each of files that is a regular file in the repository's
// working tree to its blob as `git add` would store it, written into the
// repository when write is set.
func (c Config) dirBlobs(ctx context.Context, files []string, write bool) (map[string]string, error) {
	var present []string
	for _, f := range files {
		if info, err := os.Lstat(filepath.Join(c.repo(), f)); err == nil && info.Mode().IsRegular() {
			present = append(present, f)
		}
	}
	blobs := map[string]string{}
	if len(present) == 0 {
		return blobs, nil
	}
	args := []string{"hash-object"}
	if write {
		args = append(args, "-w")
	}
	out, err := c.git(ctx, append(append(args, "--"), present...)...)
	if err != nil {
		return nil, err
	}
	shas := strings.Split(out, "\n")
	if len(shas) != len(present) {
		return nil, fmt.Errorf("git hash-object gave %d objects for %d files", len(shas), len(present))
	}
	for i, f := range present {
		blobs[f] = shas[i]
	}
	return blobs, nil
}

// target is the branch of origin's that sync sends to and takes from: the
// one origin's HEAD names, or else the one checked out.
func (c Config) target(ctx context.Context) (string, error) {
	if ref, err := c.git(ctx, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if branch, ok := strings.CutPrefix(ref, "origin/"); ok {
			return branch, nil
		}
	}
	return c.git(ctx, "symbolic-ref", "--short", "HEAD")
}

// Sync sends this host's plan files, and nothing else, as a commit of its
// own on top of origin's branch, and takes the other hosts' plans by
// bringing the checked-out branch forward, or catching it up as catchUp
// says. It never rebases or commits on the operator's branch, so no commit
// of the operator's is ever sent or re-committed, whatever they do in the
// repository meanwhile. With PullOnly it only takes the other hosts' plans,
// and never catches up.
func (c Config) Sync(o SyncOptions) (Synced, error) {
	time.Sleep(o.Settle)
	s := Synced{Repo: c.repo()}
	if !c.inRepo() {
		s.Skipped = fmt.Sprintf("%s is not a git repository's top level; `agent-plan init` clones it", c.repo())
		return s, nil
	}
	lock, err := c.lockSync(false)
	if err != nil {
		return s, err
	}
	defer lock.Close()
	ctx := context.Background()
	branch, err := c.target(ctx)
	if err != nil {
		return s, err
	}
	timed, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	if _, err := c.git(timed, "fetch", "-q", "origin"); err != nil {
		return s, err
	}
	tip, err := c.git(ctx, "rev-parse", "-q", "--verify", "refs/remotes/origin/"+branch+"^{commit}")
	if err != nil {
		s.Skipped = fmt.Sprintf("origin has no branch %s to sync with", branch)
		return s, nil
	}
	if o.PullOnly {
		s.Behind, err = c.bringForward(ctx, branch, tip)
		return s, err
	}
	if s.Behind, err = c.forward(ctx, branch, tip); err != nil {
		return s, err
	}
	// Only a checkout that holds origin's latest has this host's latest plan
	// files: in an older one, such as an older commit checked out or a
	// stash on a branch behind origin, they may be older than origin's, and
	// sending them would roll origin back.
	if _, err := c.git(ctx, "merge-base", "--is-ancestor", tip, "HEAD"); err != nil {
		if s.Behind == "" {
			s.Behind = fmt.Sprintf("HEAD does not hold origin's %s", branch)
		}
		return s, nil
	}
	sent, changed, err := c.commitOnto(ctx, tip)
	if err != nil || sent == "" {
		return s, err
	}
	// The commit just made, not a ref, so nothing else goes with it; and
	// not forced, so it adds to origin's branch and never replaces it.
	pushing, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	if _, err := c.git(pushing, "push", "-q", "origin", sent+":refs/heads/"+branch); err != nil {
		return s, err
	}
	s.Committed, s.Pushed = changed, true
	s.Behind, err = c.forward(ctx, branch, sent)
	return s, err
}

// forward is bringForward, followed by catchUp when git refuses the
// fast-forward, so that a plan file written after sync sent it cannot keep
// the branch behind for good.
func (c Config) forward(ctx context.Context, branch, tip string) (string, error) {
	behind, err := c.bringForward(ctx, branch, tip)
	if err != nil || behind == "" {
		return behind, err
	}
	if caught, err := c.catchUp(ctx, branch, tip); err != nil || caught {
		return "", err
	}
	return behind, nil
}

// catchUp moves branch and the index to tip, and leaves the working tree as
// it is, when branch is checked out, tip holds it, the commits tip adds
// change nothing but this host's plan files, nothing is staged, and no
// merge, rebase, cherry-pick, revert or bisect is in progress. Those
// commits are sync's own, so the working tree holds the same version of
// each of those files or a newer one, which sync sends next: what git does
// for `git reset tip` with nothing staged. It reports whether it moved
// branch.
//
// It holds the index's lock, as git does to write the index, from before
// it reads HEAD until it has written the new index, so the operator's git
// commands that write the index wait out or fail on it; it builds that
// index from the one the lock froze, with only the changed plan files taken
// from tip; and it moves branch only if branch still holds the commit HEAD
// held under the lock. Anything that changes the branch or the index
// meanwhile makes it change nothing.
//
// SIGTERM and SIGINT wait while it runs: dying with the lock held would
// block every git command that writes the index, and dying between moving
// branch and writing the index would leave this host's plans staged back
// to older versions. One that comes before it moves branch stops it there,
// and one that comes later once the index is written; either way the
// process then dies of it, with the lock gone.
func (c Config) catchUp(ctx context.Context, branch, tip string) (bool, error) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	defer func() {
		signal.Stop(stop)
		select {
		case s := <-stop:
			// Nothing relays it now, so it takes its default action.
			if s, ok := s.(syscall.Signal); ok {
				_ = syscall.Kill(os.Getpid(), s)
			}
		default:
		}
	}()
	index, err := c.git(ctx, "rev-parse", "--git-path", "index")
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(index) {
		index = filepath.Join(c.repo(), index)
	}
	lock := index + ".lock"
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // git's own lock on the index
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Removed unless renamed over the index: after that the name is
	// anyone's to take again.
	held := true
	defer func() {
		if held {
			os.Remove(lock)
		}
	}()
	data, err := os.ReadFile(index)
	if err == nil {
		_, err = f.Write(data)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if head, err := c.git(ctx, "symbolic-ref", "-q", "--short", "HEAD"); err != nil || head != branch {
		return false, nil
	}
	at, err := c.git(ctx, "rev-parse", "-q", "--verify", "HEAD^{commit}")
	if err != nil || at == tip {
		return false, nil
	}
	if _, err := c.git(ctx, "merge-base", "--is-ancestor", at, tip); err != nil {
		return false, nil
	}
	if op, err := c.inProgress(ctx); err != nil || op != "" {
		return false, err
	}
	rel, err := filepath.Rel(c.repo(), c.Dir)
	if err != nil {
		return false, err
	}
	// Each merge against each of its parents, whatever log.diffMerges
	// says, so that no change a merge brings goes unlisted.
	incoming, err := c.gitRaw(ctx, "log", "--format=", "--name-only", "--no-renames", "--diff-merges=separate", "-z", at+".."+tip)
	if err != nil {
		return false, err
	}
	for _, f := range strings.Split(incoming, "\x00") {
		if f != "" && (filepath.Dir(f) != rel || !c.ownName(filepath.Base(f))) {
			return false, nil
		}
	}
	env := []string{"GIT_INDEX_FILE=" + lock, "GIT_LITERAL_PATHSPECS=1"}
	if _, err := c.gitAs(ctx, env, "diff-index", "--cached", "--quiet", at, "--"); err != nil {
		return false, nil
	}
	differ, err := c.gitRaw(ctx, "diff", "--name-only", "--no-renames", "-z", at, tip)
	if err != nil {
		return false, err
	}
	if files := strings.FieldsFunc(differ, func(r rune) bool { return r == 0 }); len(files) > 0 {
		if _, err := c.gitAs(ctx, env, append([]string{"reset", "-q", tip, "--"}, files...)...); err != nil {
			return false, err
		}
	}
	select {
	case s := <-stop:
		stop <- s
		return false, nil
	default:
	}
	if _, err := c.git(ctx, "update-ref", "-m", "agent-plan sync: catch up with origin", "refs/heads/"+branch, tip, at); err != nil {
		// A signal to sync's process group can stop update-ref once it
		// has moved branch, which the index must then follow.
		if now, _ := c.git(ctx, "rev-parse", "-q", "--verify", "refs/heads/"+branch); now != tip {
			return false, nil
		}
	}
	if err := os.Rename(lock, index); err != nil {
		if _, uerr := c.git(ctx, "update-ref", "-m", "agent-plan sync: undo the catch-up", "refs/heads/"+branch, at, tip); uerr != nil {
			err = errors.Join(err, uerr)
		}
		return false, err
	}
	held = false
	return true, nil
}

// commitOnto makes a commit, as sync, whose parent is base and whose tree is
// base's with this host's plan files as they are in the plans directory, and
// returns it with the files it changed; "" when it would change none. It
// builds the tree in an index of its own, so the repository's index, working
// tree and branches stay as they are.
func (c Config) commitOnto(ctx context.Context, base string) (commit string, changed []string, err error) {
	files, err := c.ownFiles(ctx, base)
	if err != nil || len(files) == 0 {
		return "", nil, err
	}
	had, err := c.treeBlobs(ctx, base, files)
	if err != nil {
		return "", nil, err
	}
	now, err := c.dirBlobs(ctx, files, true)
	if err != nil {
		return "", nil, err
	}
	update := []string{"update-index", "--add"}
	var gone []string
	for _, f := range files {
		switch {
		case now[f] == had[f]:
			continue
		case now[f] == "":
			gone = append(gone, f)
		default:
			update = append(update, "--cacheinfo", "100644,"+now[f]+","+f)
		}
		changed = append(changed, f)
	}
	if len(changed) == 0 {
		return "", nil, nil
	}
	if len(gone) > 0 {
		update = append(append(update, "--force-remove", "--"), gone...)
	}
	dir, err := os.MkdirTemp("", "agent-plan-sync-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(dir)
	env := append(c.asSync(ctx), "GIT_INDEX_FILE="+filepath.Join(dir, "index"))
	if _, err := c.gitAs(ctx, env, "read-tree", base); err != nil {
		return "", nil, err
	}
	if _, err := c.gitAs(ctx, env, update...); err != nil {
		return "", nil, err
	}
	tree, err := c.gitAs(ctx, env, "write-tree")
	if err != nil {
		return "", nil, err
	}
	commit, err = c.gitAs(ctx, env, "commit-tree", tree, "-p", base, "-m", "Sync plans from "+c.Host)
	return commit, changed, err
}

// bringForward moves branch to tip by a fast-forward, when branch is
// checked out and tip holds it, and returns why it left branch where it was
// otherwise, "" when branch is at tip. git refuses to fast-forward over this
// host's plan files as the working tree holds them once sync has sent them,
// unstaged, so it first stages those whose content tip already has, and the
// removal of those tip and the working tree have both dropped. It does so
// only when nothing at a plan path is staged and no merge, rebase,
// cherry-pick, revert or bisect is in progress, so that it can unstage
// them again, by resetting them to HEAD, if git refuses the fast-forward;
// one that succeeds leaves them matching HEAD. A fast-forward neither
// changes nor drops a commit, and git refuses one that would overwrite an
// uncommitted change.
func (c Config) bringForward(ctx context.Context, branch, tip string) (string, error) {
	if head, err := c.git(ctx, "symbolic-ref", "-q", "--short", "HEAD"); err != nil || head != branch {
		return fmt.Sprintf("HEAD is not on %s", branch), nil
	}
	at, err := c.git(ctx, "rev-parse", "-q", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Sprintf("%s has no commit", branch), nil
	}
	if at == tip {
		return "", nil
	}
	if _, err := c.git(ctx, "merge-base", "--is-ancestor", at, tip); err != nil {
		return fmt.Sprintf("%s has commits origin lacks", branch), nil
	}
	if op, err := c.inProgress(ctx); err != nil || op != "" {
		return fmt.Sprintf("a %s is in progress", op), err
	}
	rel, err := filepath.Rel(c.repo(), c.Dir)
	if err != nil {
		return "", err
	}
	if _, err := c.git(ctx, "diff", "--cached", "--quiet", at, "--", rel); err != nil {
		return "changes to plan files are staged", nil
	}
	files, err := c.ownFiles(ctx, at, tip)
	if err != nil {
		return "", err
	}
	want, err := c.treeBlobs(ctx, tip, files)
	if err != nil {
		return "", err
	}
	now, err := c.dirBlobs(ctx, files, false)
	if err != nil {
		return "", err
	}
	var add, drop []string
	for _, f := range files {
		switch {
		case now[f] != "" && now[f] == want[f]:
			add = append(add, f)
		case now[f] == "" && want[f] == "":
			drop = append(drop, f)
		}
	}
	if len(add) > 0 {
		if _, err := c.git(ctx, append([]string{"add", "--"}, add...)...); err != nil {
			return "", err
		}
	}
	if len(drop) > 0 {
		if _, err := c.git(ctx, append([]string{"rm", "-q", "--cached", "--ignore-unmatch", "--"}, drop...)...); err != nil {
			return "", err
		}
	}
	if _, err := c.git(ctx, "merge", "-q", "--ff-only", "--no-autostash", tip); err != nil {
		if staged := append(add, drop...); len(staged) > 0 {
			if _, err := c.git(ctx, append([]string{"reset", "-q", "HEAD", "--"}, staged...)...); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("%s did not fast-forward: %v", branch, err), nil
	}
	return "", nil
}

// inProgress names the merge, rebase, cherry-pick, revert or bisect in
// progress in the repository, "" for none.
func (c Config) inProgress(ctx context.Context) (string, error) {
	ops := []struct{ path, name string }{
		{"MERGE_HEAD", "merge"}, {"rebase-merge", "rebase"}, {"rebase-apply", "rebase"},
		{"CHERRY_PICK_HEAD", "cherry-pick"}, {"REVERT_HEAD", "revert"}, {"BISECT_LOG", "bisect"},
	}
	args := []string{"rev-parse"}
	for _, op := range ops {
		args = append(args, "--git-path", op.path)
	}
	out, err := c.git(ctx, args...)
	if err != nil {
		return "", err
	}
	paths := strings.Split(out, "\n")
	if len(paths) != len(ops) {
		return "", fmt.Errorf("git rev-parse gave %d paths for %d", len(paths), len(ops))
	}
	for i, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(c.repo(), p)
		}
		if _, err := os.Lstat(p); err == nil {
			return ops[i].name, nil
		}
	}
	return "", nil
}

// Pull takes the other hosts' plans, quietly and briefly, as Sync's
// PullOnly does, and gives up on anything else, including a sync in
// progress or a repository this process may not write.
func (c Config) Pull() {
	if !c.inRepo() || !c.writableGitDir() {
		return
	}
	lock, err := c.lockSync(true)
	if err != nil || lock == nil {
		return
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), pullTimeout)
	defer cancel()
	branch, err := c.target(ctx)
	if err != nil {
		return
	}
	if _, err := c.git(ctx, "fetch", "-q", "origin"); err != nil {
		return
	}
	if tip, err := c.git(ctx, "rev-parse", "-q", "--verify", "refs/remotes/origin/"+branch+"^{commit}"); err == nil {
		c.bringForward(ctx, branch, tip)
	}
}

// Committed is the committer time, with its zone, of the newest commit on
// HEAD that touches p's log: for another host's plan, when its last change
// arrived, since a log's own times carry no zone. It is zero outside a
// repository or without such a commit.
func (c Config) Committed(p Plan) (time.Time, error) {
	if !c.inRepo() {
		return time.Time{}, nil
	}
	ctx := context.Background()
	if _, err := c.git(ctx, "rev-parse", "-q", "--verify", "HEAD"); err != nil {
		return time.Time{}, nil
	}
	rel, err := filepath.Rel(c.repo(), filepath.Join(c.Dir, p.Name+".log.org"))
	if err != nil {
		return time.Time{}, err
	}
	at, err := c.git(ctx, "log", "-1", "--format=%cI", "HEAD", "--", rel)
	if err != nil || at == "" {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, at)
}

// SyncState is how far this host's plans have reached the remote.
type SyncState struct {
	// Repo is whether the plans directory is in its repository.
	Repo bool
	// Pushed is the committer time of the newest commit of this host's
	// plan files on the remote-tracking branch, zero without one.
	Pushed time.Time
	// Unsynced is whether this host's plan files differ from the
	// remote-tracking branch's.
	Unsynced bool
	// Fetched is when the repository last fetched, zero if never or if
	// the last fetch failed.
	Fetched time.Time
}

// SyncState reads the repository alone, never the remote.
func (c Config) SyncState() (SyncState, error) {
	var s SyncState
	if !c.inRepo() {
		return s, nil
	}
	s.Repo = true
	ctx := context.Background()
	if path, err := c.git(ctx, "rev-parse", "--git-path", "FETCH_HEAD"); err == nil {
		if !filepath.IsAbs(path) {
			path = filepath.Join(c.repo(), path)
		}
		// A failed fetch empties FETCH_HEAD and still touches it.
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			s.Fetched = info.ModTime()
		}
	}
	branch, err := c.target(ctx)
	if err != nil {
		return s, err
	}
	remote := "refs/remotes/origin/" + branch
	if _, err := c.git(ctx, "rev-parse", "-q", "--verify", remote); err != nil {
		files, err := c.ownFiles(ctx)
		s.Unsynced = len(files) > 0
		return s, err
	}
	files, err := c.ownFiles(ctx, remote)
	if err != nil || len(files) == 0 {
		return s, err
	}
	at, err := c.git(ctx, append([]string{"log", "-1", "--format=%cI", remote, "--"}, files...)...)
	if err != nil {
		return s, err
	}
	if at != "" {
		if s.Pushed, err = time.Parse(time.RFC3339, at); err != nil {
			return s, err
		}
	}
	had, err := c.treeBlobs(ctx, remote, files)
	if err != nil {
		return s, err
	}
	now, err := c.dirBlobs(ctx, files, false)
	if err != nil {
		return s, err
	}
	for _, f := range files {
		s.Unsynced = s.Unsynced || now[f] != had[f]
	}
	return s, nil
}

// writableGitDir reports whether this process may write the repository's
// git directory, which a sandbox's may not.
func (c Config) writableGitDir() bool {
	dir, err := c.git(context.Background(), "rev-parse", "--absolute-git-dir")
	if err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".agent-plan-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// clone makes the notes repository where it is missing, through SSHConfig's
// ssh, and configures it, or an existing one. It returns why it did not
// clone, if it did not.
func (c Config) clone() (cloned bool, note string, err error) {
	repo := c.repo()
	if c.inRepo() {
		return false, "", c.configure()
	}
	if c.Remote == "" {
		return false, "", nil
	}
	if c.Key != "" {
		if _, err := os.Stat(c.Key); errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Sprintf("the deploy key %s is missing, so %s was not cloned and plans are not synced", c.Key, c.Remote), nil
		}
	}
	if entries, err := os.ReadDir(repo); err == nil && len(entries) > 0 {
		cleared, err := c.clearInitFiles()
		if err != nil {
			return false, "", err
		}
		if !cleared {
			return false, fmt.Sprintf("%s exists and is not a clone of %s; plans are not synced until it is", repo, c.Remote), nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil { //nolint:gosec // the notes repository's parent, made as git clone would make it
		return false, "", err
	}
	args := []string{"-c", "core.hooksPath=/dev/null"}
	if c.SSHConfig != "" {
		args = append(args, "-c", "core.sshCommand="+c.sshCommand())
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append(args, "clone", "-q", c.Remote, repo)...)
	cmd.Env = append(gitenv.Without(os.Environ()), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Sprintf("cloning %s failed, so plans are not synced: %s", c.Remote, strings.TrimSpace(string(out))), nil
	}
	return true, "", c.configure()
}

// clearInitFiles removes only the exact ignore file and directory chain
// Init creates. It validates the entire tree before removing anything;
// unrelated contents and symbolic links leave the destination untouched.
func (c Config) clearInitFiles() (bool, error) {
	repo, dir := filepath.Clean(c.repo()), filepath.Clean(c.Dir)
	rel, err := filepath.Rel(repo, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, nil
	}
	var paths []string
	valid := true
	sawIgnore := false
	err = filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if path != dir && !strings.HasPrefix(dir, path+string(filepath.Separator)) {
				valid = false
				return fs.SkipAll
			}
		case path == filepath.Join(dir, ".gitignore") && d.Type().IsRegular():
			data, err := readPlan(path)
			if err != nil || string(data) != ignore {
				valid = false
				return fs.SkipAll
			}
			sawIgnore = true
		default:
			valid = false
			return fs.SkipAll
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil || !valid || !sawIgnore {
		return false, err
	}
	for i := len(paths) - 1; i >= 0; i-- {
		if err := os.Remove(paths[i]); err != nil {
			return false, err
		}
	}
	return true, nil
}

// configure sets the notes repository's own config: unsigned commits, and
// origin at Remote when there is one. Activation runs it on every switch, so
// a host's clone follows Remote. It sets no core.sshCommand, with which git
// sign-pass refuses the repository, since sync and clone pass SSHConfig's
// ssh on each command; it takes out the one an earlier configure wrote for
// this SSHConfig, and leaves any other.
func (c Config) configure() error {
	ctx := context.Background()
	if _, err := c.git(ctx, "config", "--local", "commit.gpgsign", "false"); err != nil {
		return err
	}
	if c.SSHConfig != "" {
		if ssh, _ := c.git(ctx, "config", "--local", "--get", "core.sshCommand"); ssh == c.sshCommand() {
			if _, err := c.git(ctx, "config", "--local", "--unset", "core.sshCommand"); err != nil {
				return err
			}
		}
	}
	if c.Remote == "" {
		return nil
	}
	url, err := c.git(ctx, "config", "--local", "--get", "remote.origin.url")
	switch {
	case err != nil:
		_, err = c.git(ctx, "remote", "add", "origin", c.Remote)
	case url != c.Remote:
		_, err = c.git(ctx, "remote", "set-url", "origin", c.Remote)
	}
	return err
}
