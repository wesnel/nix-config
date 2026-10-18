package plans

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// notes makes a bare origin holding a notes repository with an agents
// directory, and a clone of it per host, each with an edit of its own that
// no sync may touch: a change to a tracked note, and a new untracked one.
func notes(t *testing.T, hosts ...string) (origin string, configs map[string]Config) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "Operator", "GIT_AUTHOR_EMAIL": "operator@example.com",
		"GIT_COMMITTER_NAME": "Operator", "GIT_COMMITTER_EMAIL": "operator@example.com"} {
		t.Setenv(k, v)
	}
	origin = filepath.Join(root, "origin.git")
	gitIn(t, root, "init", "-q", "--bare", "-b", "main", origin)
	seed := filepath.Join(root, "seed")
	gitIn(t, root, "clone", "-q", origin, seed)
	os.MkdirAll(filepath.Join(seed, "agents"), 0o755)
	os.WriteFile(filepath.Join(seed, "agents", ".gitignore"), []byte(ignore), 0o644)
	os.WriteFile(filepath.Join(seed, "todo.org"), []byte("* TODO x\n"), 0o644)
	gitIn(t, seed, "add", "agents/.gitignore", "todo.org")
	gitIn(t, seed, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "Start")
	gitIn(t, seed, "push", "-q", "origin", "main")
	configs = map[string]Config{}
	for _, h := range hosts {
		clone := filepath.Join(root, h)
		gitIn(t, root, "clone", "-q", origin, clone)
		os.WriteFile(filepath.Join(clone, "todo.org"), []byte("* TODO x, edited on "+h+"\n"), 0o644)
		os.WriteFile(filepath.Join(clone, "scratch.org"), []byte("untracked\n"), 0o644)
		configs[h] = Config{Dir: filepath.Join(clone, "agents"), Host: h}
	}
	return origin, configs
}

// untouched checks that a host's own edits are still there, uncommitted.
func untouched(t *testing.T, c Config) {
	t.Helper()
	repo := filepath.Dir(c.Dir)
	if data, _ := os.ReadFile(filepath.Join(repo, "todo.org")); string(data) != "* TODO x, edited on "+c.Host+"\n" {
		t.Errorf("%s: todo.org is %q", c.Host, data)
	}
	status := gitIn(t, repo, "status", "--porcelain")
	if !strings.Contains(status, " M todo.org") || !strings.Contains(status, "?? scratch.org") {
		t.Errorf("%s: the unrelated edits are not left as they were:\n%s", c.Host, status)
	}
}

func TestSync(t *testing.T) {
	t.Run("two hosts", testSyncTwoHosts)
	t.Run("without a repository", testSyncWithoutRepository)
	t.Run("peers", testSyncPeers)
}

// Two hosts sync disjoint plans through one origin, each committing only
// its own files, and leave every other edit alone.
func testSyncTwoHosts(t *testing.T) {
	origin, hosts := notes(t, "mac", "box")
	mac, box := hosts["mac"], hosts["box"]
	mine := newPlan(t, mac, "x")
	addItems(t, mine.file(""), "** Steps", "S1")
	if _, err := mine.Log(Event{Kind: "started", Item: "S1"}, day); err != nil {
		t.Fatal(err)
	}
	got, err := mac.Sync(SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Pushed || len(got.Committed) != 2 {
		t.Errorf("mac's sync: %+v", got)
	}
	untouched(t, mac)
	theirs := newPlan(t, box, "y")
	if got, err := box.Sync(SyncOptions{}); err != nil || !got.Pushed {
		t.Fatalf("box's sync: %+v, %v", got, err)
	}
	untouched(t, box)
	if _, err := os.Stat(filepath.Join(box.Dir, "2026-10-14-x.mac.org")); err != nil {
		t.Errorf("box did not get mac's plan: %v", err)
	}
	if got, err := mac.Sync(SyncOptions{PullOnly: true}); err != nil || got.Pushed || len(got.Committed) != 0 {
		t.Fatalf("mac's pull: %+v, %v", got, err)
	}
	untouched(t, mac)
	p, err := mac.Open(filepath.Join(mac.Dir, filepath.Base(theirs.file(""))))
	if err != nil || p.Own {
		t.Errorf("mac reading box's plan: %+v, %v", p, err)
	}
	log := gitIn(t, origin, "log", "--format=%an %G? %s", "main")
	if log != "Operator N Sync plans from box\nOperator N Sync plans from mac\nOperator N Start\n" {
		t.Errorf("origin's history:\n%s", log)
	}
	for rev, host := range map[string]string{"main": "box", "main~1": "mac"} {
		files := strings.Fields(gitIn(t, origin, "show", "--format=", "--name-only", rev))
		for _, f := range files {
			if m := planFile.FindStringSubmatch(filepath.Base(f)); m == nil || m[2] != host {
				t.Errorf("%s's commit holds %s", host, f)
			}
		}
		if len(files) == 0 {
			t.Errorf("%s's commit holds nothing", host)
		}
	}
	// Nothing new: no commit, no push.
	if got, err := mac.Sync(SyncOptions{}); err != nil || got.Pushed || len(got.Committed) != 0 {
		t.Errorf("a sync with nothing to send: %+v, %v", got, err)
	}
}

// A plans directory outside any repository has nothing to sync with.
func testSyncWithoutRepository(t *testing.T) {
	c := testConfig(t)
	newPlan(t, c, "x")
	got, err := c.Sync(SyncOptions{})
	if err != nil || got.Skipped == "" || got.Pushed {
		t.Errorf("sync outside a repository: %+v, %v", got, err)
	}
	c.Pull()
}

// fakeSSH puts an ssh on PATH that records its arguments, and has a master
// connection when master is true.
func fakeSSH(t *testing.T, master bool) (record string) {
	t.Helper()
	bin := t.TempDir()
	record = filepath.Join(bin, "calls")
	exit := "1"
	if master {
		exit = "0"
	}
	script := "#!/bin/sh\necho \"$*\" >> " + record + "\ncase \"$*\" in *'-O check'*) exit " + exit + ";; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

// After a push, a peer is told to pull, but only over a master connection
// that already exists, and offered no key.
func testSyncPeers(t *testing.T) {
	// synced makes a host, mac, with one peer, devbox, reached through an
	// ssh that records its calls, and syncs a new plan of mac's. It returns
	// the calls ssh recorded, and how to reach the record.
	synced := func(t *testing.T, master bool) (Config, Synced, []string, string) {
		t.Helper()
		_, hosts := notes(t, "mac")
		mac := hosts["mac"]
		mac.Peers = []Peer{{SSH: "devbox", Command: "/home/w/.nix-profile/bin/agent-plan"}}
		record := fakeSSH(t, master)
		newPlan(t, mac, "x")
		got, err := mac.Sync(SyncOptions{})
		if err != nil || !got.Pushed {
			t.Fatalf("sync: %+v, %v", got, err)
		}
		data, _ := os.ReadFile(record)
		calls := strings.Split(strings.TrimSpace(string(data)), "\n")
		if calls[0] != "-o BatchMode=yes -O check devbox" {
			t.Errorf("first ssh call %q", calls[0])
		}
		return mac, got, calls, record
	}

	t.Run("with a master", func(t *testing.T) {
		mac, got, calls, record := synced(t, true)
		want := "-o ControlMaster=no -o BatchMode=yes -o ConnectTimeout=10 -o IdentityAgent=none -o IdentityFile=none " +
			"-o ForwardAgent=no -o ClearAllForwardings=yes devbox /home/w/.nix-profile/bin/agent-plan sync --pull"
		if len(calls) != 2 || calls[1] != want || got.Peers["devbox"] != "pulled" {
			t.Errorf("with a master: %q, %+v", calls, got.Peers)
		}
		// A pull alone tells nobody.
		os.Remove(record)
		if _, err := mac.Sync(SyncOptions{PullOnly: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(record); err == nil {
			t.Error("a pull ran ssh")
		}
	})

	t.Run("without a master", func(t *testing.T) {
		_, got, calls, _ := synced(t, false)
		if len(calls) != 1 || !strings.HasPrefix(got.Peers["devbox"], "skipped") {
			t.Errorf("without a master: %q, %+v", calls, got.Peers)
		}
	})
}

// Publish commits the files it is given, by name, and this host's plans,
// and pushes both; it writes nothing a link leads outside the repository,
// nor into the plans directory, and leaves every other edit alone.
func TestPublish(t *testing.T) {
	origin, hosts := notes(t, "box")
	box := hosts["box"]
	repo := filepath.Dir(box.Dir)
	newPlan(t, box, "x")
	// Staged by someone else: neither committed nor pushed.
	os.WriteFile(filepath.Join(repo, "staged.org"), []byte("staged\n"), 0o644)
	gitIn(t, repo, "add", "staged.org")
	digest := map[string][]byte{"digests/2026/10-14-standup.box.org": []byte("#+title: Standup\n")}
	got, err := box.Publish(digest, "Add the standup digest")
	if err != nil || !got.Wrote || !got.Pushed || len(got.Committed) != 2 {
		t.Fatalf("publish: %+v, %v", got, err)
	}
	if log := gitIn(t, origin, "log", "--format=%G? %s", "main"); log != "N Add the standup digest\nN Sync plans from box\nN Start\n" {
		t.Errorf("origin's history:\n%s", log)
	}
	if files := gitIn(t, origin, "show", "--format=", "--name-only", "main"); files != "digests/2026/10-14-standup.box.org\n" {
		t.Errorf("the digest's commit holds %q", files)
	}
	if files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main"); strings.Contains(files, "staged.org") {
		t.Errorf("pushed the staged file:\n%s", files)
	}
	if status := gitIn(t, repo, "status", "--porcelain"); !strings.Contains(status, "A  staged.org") {
		t.Errorf("the staged file is no longer staged:\n%s", status)
	}
	untouched(t, box)

	// The same content again: no commit, nothing to push.
	if got, err := box.Publish(digest, "Add the standup digest"); err != nil || got.Wrote || got.Pushed {
		t.Errorf("publishing it again: %+v, %v", got, err)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "linked")); err != nil {
		t.Fatal(err)
	}
	// Links inside the repository, as another host could commit them.
	os.Symlink("agents", filepath.Join(repo, "into-plans"))
	os.Symlink(".git", filepath.Join(repo, "into-git"))
	for _, name := range []string{"linked/x.org", "into-plans/x.org", "into-git/x.org", ".git/x.org", ".GIT/x.org",
		"../x.org", "/tmp/x.org", "digests/../x.org", "agents/x.org", ""} {
		if got, err := box.Publish(map[string][]byte{name: []byte("x\n")}, "Refused"); err == nil || got.Wrote {
			t.Errorf("%q: %+v, %v", name, got, err)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("wrote through the link: %v", entries)
	}
	for _, f := range []string{filepath.Join(box.Dir, "x.org"), filepath.Join(repo, ".git", "x.org")} {
		if _, err := os.Lstat(f); err == nil {
			t.Errorf("wrote %s", f)
		}
	}
	if log := gitIn(t, origin, "log", "-1", "--format=%s", "main"); log != "Add the standup digest\n" {
		t.Errorf("origin moved: %q", log)
	}

	// A commit that fails, here for its empty message, leaves nothing
	// staged.
	if got, err := box.Publish(map[string][]byte{"digests/2026/10-16-standup.box.org": []byte("x\n")}, ""); err == nil || got.Wrote {
		t.Errorf("a failing commit: %+v, %v", got, err)
	}
	if staged := gitIn(t, repo, "diff", "--cached", "--name-only"); staged != "staged.org\n" {
		t.Errorf("staged after a failed commit: %q", staged)
	}

	// Without a remote to push to, the commit stays for the next sync.
	gitIn(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	got, err = box.Publish(map[string][]byte{"digests/2026/10-15-standup.box.org": []byte("#+title: Standup\n")}, "Add another")
	if !errors.Is(err, ErrUnsent) || !got.Wrote || got.Pushed {
		t.Errorf("publishing unreachable: %+v, %v", got, err)
	}
	if log := gitIn(t, repo, "log", "-1", "--format=%s"); log != "Add another\n" {
		t.Errorf("the commit did not stay: %q", log)
	}

	if got, err := testConfig(t).Publish(digest, "x"); err != nil || got.Skipped == "" {
		t.Errorf("outside a repository: %+v, %v", got, err)
	}
}

// Publish writes nothing while a sync holds the lock.
func TestPublishLock(t *testing.T) {
	_, hosts := notes(t, "box")
	box := hosts["box"]
	file := filepath.Join(filepath.Dir(box.Dir), "digests", "d.box.org")
	lock, err := box.lockSync(false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() {
		_, err := box.Publish(map[string][]byte{"digests/d.box.org": []byte("x\n")}, "Add")
		done <- err
	}()
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(file); err == nil {
		t.Error("wrote while the lock was held")
	}
	lock.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Error(err)
	}
}

// Where the remote holds another version of a published file, the rebase
// fails: Publish drops its own commit, so that later syncs are not stuck
// behind it, says so, and leaves what else is staged as it was.
func TestPublishConflict(t *testing.T) {
	origin, hosts := notes(t, "mac", "box")
	mac, box := hosts["mac"], hosts["box"]
	if _, err := mac.Publish(map[string][]byte{"digests/2026/10-14-standup.org": []byte("mac\n")}, "Add mac's"); err != nil {
		t.Fatal(err)
	}
	newPlan(t, box, "x")
	repo := filepath.Dir(box.Dir)
	os.WriteFile(filepath.Join(repo, "staged.org"), []byte("staged\n"), 0o644)
	gitIn(t, repo, "add", "staged.org")
	got, err := box.Publish(map[string][]byte{"digests/2026/10-14-standup.org": []byte("box\n")}, "Add box's")
	if !errors.Is(err, errRebase) || errors.Is(err, ErrUnsent) || got.Wrote || got.Pushed || got.Dropped == "" {
		t.Errorf("box's publish: %+v, %v", got, err)
	}
	if log := gitIn(t, repo, "log", "-1", "--format=%s"); log != "Sync plans from box\n" {
		t.Errorf("box's HEAD: %q", log)
	}
	if status := gitIn(t, repo, "status", "--porcelain", "--", "digests"); status != "" {
		t.Errorf("left behind: %q", status)
	}
	if staged := gitIn(t, repo, "diff", "--cached", "--name-status"); staged != "A\tstaged.org\n" {
		t.Errorf("staged after the drop: %q", staged)
	}
	gitIn(t, repo, "reset", "-q", "--", "staged.org")
	untouched(t, box)
	newPlan(t, box, "y")
	if s, err := box.Sync(SyncOptions{}); err != nil || !s.Pushed {
		t.Errorf("box's next sync: %+v, %v", s, err)
	}
	if data := gitIn(t, origin, "show", "main:digests/2026/10-14-standup.org"); data != "mac\n" {
		t.Errorf("origin's digest: %q", data)
	}
}

// A rebase that fails on something else, here the operator's own commit to
// a note that the remote changed too, keeps the published commit and names
// the conflict.
func TestPublishConflictElsewhere(t *testing.T) {
	_, hosts := notes(t, "mac", "box")
	mac, box := hosts["mac"], hosts["box"]
	for _, c := range []Config{mac, box} {
		repo := filepath.Dir(c.Dir)
		gitIn(t, repo, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "Edit on "+c.Host, "--", "todo.org")
	}
	if s, err := mac.Sync(SyncOptions{}); err != nil || !s.Pushed {
		t.Fatalf("mac's sync: %+v, %v", s, err)
	}
	got, err := box.Publish(map[string][]byte{"digests/2026/10-14-standup.box.org": []byte("box\n")}, "Add box's")
	if !errors.Is(err, errRebase) || !strings.Contains(err.Error(), "todo.org") || !got.Wrote || got.Dropped != "" {
		t.Errorf("box's publish: %+v, %v", got, err)
	}
	if log := gitIn(t, filepath.Dir(box.Dir), "log", "-1", "--format=%s"); log != "Add box's\n" {
		t.Errorf("box's HEAD: %q", log)
	}
}

// drop takes out the files' commit only while it is still HEAD.
func TestDrop(t *testing.T) {
	_, hosts := notes(t, "box")
	box := hosts["box"]
	repo := filepath.Dir(box.Dir)
	ctx := context.Background()
	os.WriteFile(filepath.Join(repo, "d.org"), []byte("d\n"), 0o644)
	gitIn(t, repo, "add", "d.org")
	gitIn(t, repo, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "Add d")
	ours := strings.TrimSpace(gitIn(t, repo, "rev-parse", "HEAD"))
	gitIn(t, repo, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "The operator's", "--", "todo.org")
	if err := box.drop(ctx, ours, []string{"d.org"}); err == nil {
		t.Error("dropped a commit that is no longer HEAD")
	}
	if log := gitIn(t, repo, "log", "-2", "--format=%s"); log != "The operator's\nAdd d\n" {
		t.Errorf("history: %q", log)
	}
	gitIn(t, repo, "checkout", "-q", "--detach", ours)
	if err := box.drop(ctx, ours, []string{"d.org"}); err == nil {
		t.Error("dropped a commit on a detached HEAD")
	}
}

// Find, status and new pull first, quietly, so another host's plans are
// current; a pull that cannot run changes nothing.
func TestPull(t *testing.T) {
	_, hosts := notes(t, "mac", "box")
	mac, box := hosts["mac"], hosts["box"]
	theirs := newPlan(t, box, "y")
	if _, err := box.Sync(SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	mac.Pull()
	if _, err := mac.Open(filepath.Join(mac.Dir, filepath.Base(theirs.file("")))); err != nil {
		t.Errorf("after a pull: %v", err)
	}
	untouched(t, mac)
}

// SyncState says when this host's plans last reached the remote, whether
// they have changed since, and when the repository last fetched; another
// host's commits and the repository's other files count for none of it.
func TestSyncState(t *testing.T) {
	origin, hosts := notes(t, "mac", "box")
	mac, box := hosts["mac"], hosts["box"]
	state := func(c Config) SyncState {
		t.Helper()
		s, err := c.SyncState()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := state(box); !s.Repo || !s.Pushed.IsZero() || s.Unsynced || !s.Fetched.IsZero() {
		t.Errorf("before any plan: %+v", s)
	}
	mine := newPlan(t, box, "x")
	if s := state(box); !s.Pushed.IsZero() || !s.Unsynced {
		t.Errorf("with a new plan: %+v", s)
	}
	pushed := time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)
	t.Setenv("GIT_COMMITTER_DATE", pushed.Format(time.RFC3339))
	if _, err := box.Sync(SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	s := state(box)
	if !s.Pushed.Equal(pushed) || s.Unsynced || s.Fetched.IsZero() {
		t.Errorf("after a sync: %+v", s)
	}

	t.Setenv("GIT_COMMITTER_DATE", pushed.Add(time.Hour).Format(time.RFC3339))
	newPlan(t, mac, "y")
	if _, err := mac.Sync(SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	box.Pull()
	if s := state(box); !s.Pushed.Equal(pushed) || s.Unsynced {
		t.Errorf("after mac's plan arrived: %+v", s)
	}

	setGoal(t, mine.file(""), "Changed after the sync.")
	if s := state(box); !s.Unsynced {
		t.Errorf("with an edit: %+v", s)
	}
	gitIn(t, filepath.Dir(box.Dir), "-c", "commit.gpgsign=false", "commit", "-q", "-am", "Commit everything")
	if s := state(box); !s.Pushed.Equal(pushed) || !s.Unsynced {
		t.Errorf("committed, not pushed: %+v", s)
	}
	if log := gitIn(t, origin, "log", "-1", "--format=%s", "main"); log != "Sync plans from mac\n" {
		t.Errorf("origin moved: %q", log)
	}

	// A fetch that fails empties FETCH_HEAD but touches it, and is no
	// fetch: an unreachable remote and a missing ref.
	repo := filepath.Dir(box.Dir)
	fetchHead := filepath.Join(repo, ".git", "FETCH_HEAD")
	good := time.Now().Add(-time.Hour).Truncate(time.Second)
	for name, args := range map[string][]string{
		"unreachable remote": {"fetch", "-q", filepath.Join(t.TempDir(), "missing.git")},
		"missing ref":        {"fetch", "-q", "origin", "refs/heads/missing"},
	} {
		gitIn(t, repo, "fetch", "-q", "origin")
		os.Chtimes(fetchHead, good, good)
		if s := state(box); !s.Fetched.Equal(good) {
			t.Errorf("%s: after a good fetch: %+v", name, s)
		}
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("%s: the fetch succeeded: %s", name, out)
		}
		if info, err := os.Stat(fetchHead); err != nil || info.Size() != 0 || !info.ModTime().After(good) {
			t.Logf("%s: FETCH_HEAD after the failed fetch: %v, %v", name, info, err)
		}
		if s := state(box); s.Fetched.After(good) {
			t.Errorf("%s: a failed fetch counts as one: %+v", name, s)
		}
	}

	if s := state(testConfig(t)); s.Repo || s.Unsynced {
		t.Errorf("outside a repository: %+v", s)
	}
}

// cloning is a host whose notes repository is missing, to be cloned from
// origin with a deploy key that does not exist until the test writes it.
func cloning(t *testing.T) (c Config, root, key string) {
	t.Helper()
	origin, _ := notes(t)
	root, _ = filepath.EvalSymlinks(t.TempDir())
	key = filepath.Join(root, "key")
	c = Config{Dir: filepath.Join(root, "notes", "agents"), Host: "mac", Remote: origin, Key: key,
		SSHConfig: "/etc/notes-ssh-config"}
	return c, root, key
}

// Without the deploy key, Init says so and clones nothing.
func testInitCloneWithoutKey(t *testing.T) {
	c, root, key := cloning(t)
	got, err := c.Init()
	if err != nil || got.Cloned || !strings.Contains(got.Note, key) {
		t.Errorf("without the key: %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes", ".git")); err == nil {
		t.Error("cloned without the key")
	}
}

// Init clones the notes repository where it is missing, with the deploy
// key, and sets it never to sign.
func testInitCloneWithKey(t *testing.T) {
	c, root, key := cloning(t)
	os.WriteFile(key, []byte("private\n"), 0o600)
	for i := 0; i < 2; i++ {
		got, err := c.Init()
		if err != nil || got.Cloned != (i == 0) || got.Repo != filepath.Join(root, "notes") {
			t.Errorf("Init %d: %+v, %v", i, got, err)
		}
	}
	repo := filepath.Join(root, "notes")
	for key, want := range map[string]string{"commit.gpgsign": "false", "core.sshCommand": "ssh -F /etc/notes-ssh-config"} {
		if got := strings.TrimSpace(gitIn(t, repo, "config", "--local", key)); got != want {
			t.Errorf("%s = %q", key, got)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "todo.org")); err != nil {
		t.Errorf("not a clone of origin: %v", err)
	}
}

// Reseal accepts a log edited outside agent-plan, and records that it did.
func TestReseal(t *testing.T) {
	withStep := func(t *testing.T) (Config, Plan) {
		t.Helper()
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Steps", "S1")
		return c, p
	}

	t.Run("intact log", func(t *testing.T) {
		_, p := withStep(t)
		_, err := p.Reseal(day)
		isRefusal(t, "resealing an intact log", err)
	})

	t.Run("edited log", func(t *testing.T) {
		_, p := withStep(t)
		data, _ := os.ReadFile(p.log())
		os.WriteFile(p.log(), append(data, "a note by hand\n"...), 0o644)
		_, err := p.Log(Event{Kind: "started", Item: "S1"}, day)
		isRefusal(t, "logging to an edited log", err)
		e, err := p.Reseal(day.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind != "resealed" || e.Item != p.Name || !strings.Contains(e.Evidence, "recorded ") {
			t.Errorf("event %+v", e)
		}
		if _, err := p.Log(Event{Kind: "started", Item: "S1"}, day); err != nil {
			t.Errorf("after resealing: %v", err)
		}
		after, _ := os.ReadFile(p.log())
		if !strings.Contains(string(after), "a note by hand\n* resealed "+p.Name+" — ") {
			t.Errorf("log:\n%s", after)
		}
	})

	t.Run("another host's log", func(t *testing.T) {
		c, _ := withStep(t)
		theirs := newPlan(t, Config{Dir: c.Dir, Host: "other"}, "y")
		other, _ := c.Open(theirs.file(""))
		_, err := other.Reseal(day)
		isRefusal(t, "resealing another host's log", err)
	})
}
