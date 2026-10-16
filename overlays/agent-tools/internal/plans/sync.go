package plans

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// Peer is a host told to pull after a sync pushes.
type Peer struct {
	// SSH is the host as ssh names it. It is reached only over a master
	// connection that already exists.
	SSH string `json:"ssh"`
	// Command is agent-plan there.
	Command string `json:"command"`
}

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
	// Peers says what became of each peer: pulled, or why not.
	Peers map[string]string `json:"peers,omitempty"`
}

const (
	// gitTimeout bounds each git command that reaches the remote.
	gitTimeout = 60 * time.Second
	// pullTimeout bounds the quiet pull before find, status and new.
	pullTimeout = 10 * time.Second
	// peerTimeout bounds a peer's pull.
	peerTimeout = 120 * time.Second
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
	pre := []string{"-C", c.repo(), "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}
	if c.SSHConfig != "" {
		pre = append(pre, "-c", "core.sshCommand="+c.sshCommand())
	}
	cmd := exec.CommandContext(ctx, "git", append(pre, args...)...)
	cmd.Env = append(gitenv.Without(os.Environ()), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
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
	f, err := os.OpenFile(filepath.Join(c.Dir, ".sync.lock"), os.O_RDWR|os.O_CREATE, 0o644)
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
// repository: those in the plans directory, and those git tracks that are
// gone from it.
func (c Config) ownFiles(ctx context.Context) ([]string, error) {
	rel, err := filepath.Rel(c.repo(), c.Dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if m := planFile.FindStringSubmatch(name); m != nil && m[2] == c.Host && !seen[name] {
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
	tracked, err := c.git(ctx, "ls-files", "--", rel)
	if err != nil {
		return nil, err
	}
	for _, f := range strings.Split(tracked, "\n") {
		if f != "" && filepath.Dir(f) == rel {
			add(filepath.Base(f))
		}
	}
	return out, nil
}

// Sync commits this host's plan files, and nothing else, takes the other
// hosts' from the remote and sends this host's, then tells each peer to
// pull. Other changes in the repository are left as they are. With
// PullOnly it only takes the other hosts' plans.
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
	if !o.PullOnly {
		if s.Committed, err = c.commitOwn(ctx); err != nil {
			return s, err
		}
	}
	branch, err := c.git(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return s, err
	}
	remote := "refs/remotes/origin/" + branch
	timed, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	if _, err := c.git(timed, "fetch", "-q", "origin"); err != nil {
		return s, err
	}
	known := true
	if _, err := c.git(ctx, "rev-parse", "-q", "--verify", remote); err != nil {
		known = false
	} else if _, err := c.git(ctx, "rebase", "-q", "--autostash", remote); err != nil {
		c.git(ctx, "rebase", "--abort")
		return s, err
	}
	if o.PullOnly {
		return s, nil
	}
	if known {
		if ahead, err := c.git(ctx, "rev-list", "--count", remote+"..HEAD"); err != nil || ahead == "0" {
			return s, err
		}
	}
	pushing, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	if _, err := c.git(pushing, "push", "-q", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return s, err
	}
	s.Pushed = true
	for _, p := range c.Peers {
		if s.Peers == nil {
			s.Peers = map[string]string{}
		}
		s.Peers[p.SSH] = nudge(p)
	}
	return s, nil
}

// commitOwn commits this host's plan files, by name, unsigned, and returns
// those it committed.
func (c Config) commitOwn(ctx context.Context) ([]string, error) {
	files, err := c.ownFiles(ctx)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	var present, gone []string
	for _, f := range files {
		if _, err := os.Lstat(filepath.Join(c.repo(), f)); err == nil {
			present = append(present, f)
		} else {
			gone = append(gone, f)
		}
	}
	if len(present) > 0 {
		if _, err := c.git(ctx, append([]string{"add", "--"}, present...)...); err != nil {
			return nil, err
		}
	}
	if len(gone) > 0 {
		if _, err := c.git(ctx, append([]string{"rm", "-q", "--cached", "--ignore-unmatch", "--"}, gone...)...); err != nil {
			return nil, err
		}
	}
	changed, err := c.git(ctx, append([]string{"diff", "--cached", "--name-only", "--"}, files...)...)
	if err != nil || changed == "" {
		return nil, err
	}
	args := append([]string{"-c", "commit.gpgsign=false", "commit", "-q", "-m", "Sync plans from " + c.Host, "--"}, files...)
	if _, err := c.git(ctx, args...); err != nil {
		return nil, err
	}
	return strings.Split(changed, "\n"), nil
}

// nudge tells a peer to pull, over the master connection to it that
// already exists: a new connection would wait for a touch of a hardware
// key. If the master goes between the check and the call, the call has no
// key to offer, and fails rather than waits.
func nudge(p Peer) string {
	checking, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if exec.CommandContext(checking, "ssh", "-o", "BatchMode=yes", "-O", "check", p.SSH).Run() != nil {
		return "skipped: no master connection"
	}
	command := p.Command
	if command == "" {
		command = "agent-plan"
	}
	ctx, cancel := context.WithTimeout(context.Background(), peerTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ssh", "-o", "ControlMaster=no", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "IdentityAgent=none", "-o", "IdentityFile=none", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes",
		p.SSH, command, "sync", "--pull").CombinedOutput()
	if err != nil {
		return fmt.Sprintf("failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return "pulled"
}

// Pull takes the other hosts' plans, quietly and briefly: it fast-forwards
// alone, so it never moves this host's own changes, and gives up on
// anything else, including a sync in progress or a repository this process
// may not write.
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
	branch, err := c.git(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return
	}
	if _, err := c.git(ctx, "fetch", "-q", "origin"); err != nil {
		return
	}
	c.git(ctx, "merge", "-q", "--ff-only", "--no-autostash", "refs/remotes/origin/"+branch)
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
	files, err := c.ownFiles(ctx)
	if err != nil || len(files) == 0 {
		return s, err
	}
	branch, err := c.git(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return s, err
	}
	remote := "refs/remotes/origin/" + branch
	if _, err := c.git(ctx, "rev-parse", "-q", "--verify", remote); err != nil {
		s.Unsynced = true
		return s, nil
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
	changed, err := c.git(ctx, append([]string{"diff", "--name-only", remote, "--"}, files...)...)
	if err != nil {
		return s, err
	}
	untracked, err := c.git(ctx, append([]string{"ls-files", "--others", "--exclude-standard", "--"}, files...)...)
	s.Unsynced = changed != "" || untracked != ""
	return s, err
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

// clone makes the notes repository where it is missing, and sets it to
// commit unsigned and reach its remote through SSHConfig. It returns why it
// did not clone, if it did not.
func (c Config) clone() (cloned bool, note string, err error) {
	repo := c.repo()
	if c.inRepo() {
		return false, "", c.configure()
	}
	if c.Remote == "" {
		return false, "", nil
	}
	if entries, err := os.ReadDir(repo); err == nil && len(entries) > 0 {
		return false, fmt.Sprintf("%s exists and is not a clone of %s; plans are not synced until it is", repo, c.Remote), nil
	}
	if c.Key != "" {
		if _, err := os.Stat(c.Key); errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Sprintf("the deploy key %s is missing, so %s was not cloned and plans are not synced", c.Key, c.Remote), nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
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

// configure sets the notes repository's own config: unsigned commits, and
// the remote's ssh.
func (c Config) configure() error {
	ctx := context.Background()
	if _, err := c.git(ctx, "config", "--local", "commit.gpgsign", "false"); err != nil {
		return err
	}
	if c.SSHConfig != "" {
		if _, err := c.git(ctx, "config", "--local", "core.sshCommand", c.sshCommand()); err != nil {
			return err
		}
	}
	return nil
}
