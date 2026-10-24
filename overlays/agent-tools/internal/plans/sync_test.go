package plans

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
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
	t.Run("through git-push-guard", testSyncThroughPushGuard)
	t.Run("leaves the operator's commits", testSyncLeavesOperatorCommits)
	t.Run("leaves a commit made while it runs", testSyncLeavesRacingCommit)
	t.Run("keeps edits and a commit made while it runs", testSyncKeepsEditsAndRacingCommit)
	t.Run("leaves a branch switched to while it runs", testSyncLeavesSwitchedBranch)
	t.Run("sends nothing behind origin", testSyncSendsNothingBehind)
	t.Run("leaves the index", testSyncLeavesIndex)
	t.Run("does not force its push", testSyncDoesNotForce)
	t.Run("without a repository", testSyncWithoutRepository)
}

// A commit the operator made or changed that the remote lacks is neither
// sent nor re-committed, by a sync or a pull-only one, and the branch that
// holds it stays where it is: re-committed under sync's identity, the commit
// would lose its signature and pass git-push-guard on the operator's next
// push as sync's. Nor does sync send this host's plans from a checkout that
// lacks origin's latest, whose plan files may be older than origin's.
func testSyncLeavesOperatorCommits(t *testing.T) {
	const sync = syncName + " <agent-plan@mac.invalid>"
	cases := map[string]struct {
		// args and env make the operator's commit.
		args, env []string
		// topic puts it on another of origin's remote-tracking refs, which
		// is not the branch the rebase replays onto.
		topic bool
		// branch is a local branch origin lacks, made for the commit.
		branch string
	}{
		"made by the operator": {},
		// A sync commit the operator amended keeps sync as its author.
		"authored by sync, committed by the operator": {args: []string{"--author", sync}},
		"authored by the operator, committed by sync": {env: []string{"GIT_COMMITTER_NAME=" + syncName, "GIT_COMMITTER_EMAIL=agent-plan@mac.invalid"}},
		"on another of origin's refs":                 {topic: true},
		"on a branch origin lacks": {branch: "local",
			env: []string{"GIT_COMMITTER_NAME=" + syncName, "GIT_COMMITTER_EMAIL=agent-plan@mac.invalid"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, hosts := guarded(t, "mac", "box")
			mac, box := hosts["mac"], hosts["box"]
			newPlan(t, box, "y")
			if got, err := box.Sync(SyncOptions{}); err != nil || !got.Pushed {
				t.Fatalf("box's sync: %+v, %v", got, err)
			}
			repo := filepath.Dir(mac.Dir)
			if tc.branch != "" {
				gitIn(t, repo, "switch", "-q", "-c", tc.branch)
			}
			commit := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "Changed by the operator"}, tc.args...)...)
			commit.Dir, commit.Env = repo, append(gitenv.Without(os.Environ()), tc.env...)
			if out, err := commit.CombinedOutput(); err != nil {
				t.Fatalf("the operator's commit: %v\n%s", err, out)
			}
			mine := strings.TrimSpace(gitIn(t, repo, "rev-parse", "HEAD"))
			if tc.topic {
				gitIn(t, repo, "update-ref", "refs/remotes/origin/topic", mine)
			}
			newPlan(t, mac, "x")
			for _, o := range []SyncOptions{{PullOnly: true}, {}} {
				got, err := mac.Sync(o)
				if err != nil || got.Pushed || got.Behind == "" {
					t.Errorf("sync %+v over the operator's commit: %+v, %v", o, got, err)
				}
				if head := strings.TrimSpace(gitIn(t, repo, "rev-parse", "HEAD")); head != mine {
					t.Errorf("sync %+v moved HEAD from the operator's commit to %s", o, head)
				}
				for _, ref := range strings.Fields(gitIn(t, repo, "branch", "-r", "--contains", mine)) {
					if ref != "origin/topic" {
						t.Errorf("sync %+v published the operator's commit to %s", o, ref)
					}
				}
				untouched(t, mac)
			}
			if sent := gitIn(t, repo, "ls-tree", "--name-only", "origin/main", "agents/2026-10-14-x.mac.org"); sent != "" {
				t.Error("mac sent its plan from a branch without origin's latest")
			}
		})
	}
}

// raceAt puts a git ahead of the real one on PATH that runs action, a shell
// command, in repo as the operator, with the repository's own index, once,
// as the first git command with an argument equal to word starts. It returns
// a file that exists once action has run.
func raceAt(t *testing.T, repo, word, action string) (ran string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	ran = filepath.Join(bin, "ran")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = '" + word + "' ] && [ ! -e '" + ran + "' ]; then\n" +
		"    : > '" + ran + "'\n" +
		"    ( cd '" + repo + "' && unset GIT_INDEX_FILE && export GIT_AUTHOR_NAME=Operator GIT_AUTHOR_EMAIL=operator@example.com" +
		" GIT_COMMITTER_NAME=Operator GIT_COMMITTER_EMAIL=operator@example.com && " + action + " ) >&2 || exit 1\n" +
		"  fi\n" +
		"done\n" +
		"exec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return ran
}

// raced fails the test unless raceAt's action ran.
func raced(t *testing.T, ran string) {
	t.Helper()
	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("the operator's action never ran: %v", err)
	}
}

// operatorsCommit checks that the commit subject names is on HEAD, authored
// and committed by the operator, and on no branch of origin's.
func operatorsCommit(t *testing.T, repo, subject string) {
	t.Helper()
	if by := gitIn(t, repo, "log", "--format=%ae %ce", "--grep="+subject, "HEAD"); by != "operator@example.com operator@example.com\n" {
		t.Errorf("%q on HEAD is authored and committed by %q", subject, by)
	}
	if on := gitIn(t, repo, "log", "--format=%s", "--grep="+subject, "--remotes=origin"); on != "" {
		t.Errorf("sync published %q", subject)
	}
}

// A commit the operator makes while sync runs, at any of its steps, is
// neither published nor re-committed under sync's identity.
func testSyncLeavesRacingCommit(t *testing.T) {
	for _, word := range []string{"commit-tree", "push", "merge-base", "merge"} {
		t.Run(word, func(t *testing.T) {
			_, hosts := guarded(t, "mac", "box")
			mac, box := hosts["mac"], hosts["box"]
			newPlan(t, box, "y")
			if got, err := box.Sync(SyncOptions{}); err != nil || !got.Pushed {
				t.Fatalf("box's sync: %+v, %v", got, err)
			}
			repo := filepath.Dir(mac.Dir)
			ran := raceAt(t, repo, word, "git -c commit.gpgsign=false commit -q --allow-empty -m 'Racing operator commit'")
			newPlan(t, mac, "x")
			got, err := mac.Sync(SyncOptions{})
			raced(t, ran)
			t.Logf("sync: %+v, %v", got, err)
			operatorsCommit(t, repo, "Racing operator commit")
			// Sync moved the branch by no merge of its own.
			if head := gitIn(t, repo, "log", "-1", "--format=%s %P", "HEAD"); len(strings.Fields(head)) != 4 || !strings.HasPrefix(head, "Racing operator commit ") {
				t.Errorf("HEAD is %q, not the operator's commit", head)
			}
			// The operator's commit was made from their own index, in which
			// sync stages nothing it leaves there.
			if files := gitIn(t, repo, "show", "--format=", "--name-only", "HEAD"); files != "" {
				t.Errorf("the operator's empty commit holds:\n%s", files)
			}
			untouched(t, mac)
		})
	}
}

// Sync sends nothing from a checkout that lacks origin's latest, such as an
// older commit checked out or a branch with commits of the operator's, and
// so never sends plan files older than origin's, whatever the operator does
// to the working tree there.
func testSyncSendsNothingBehind(t *testing.T) {
	t.Run("older commit checked out", func(t *testing.T) {
		_, hosts := notes(t, "mac")
		mac := hosts["mac"]
		repo := filepath.Dir(mac.Dir)
		newPlan(t, mac, "x")
		if got, err := mac.Sync(SyncOptions{}); err != nil || !got.Pushed {
			t.Fatalf("mac's sync: %+v, %v", got, err)
		}
		sent := gitIn(t, repo, "rev-parse", "origin/main")
		gitIn(t, repo, "checkout", "-q", "--detach", "HEAD~1")
		newPlan(t, mac, "z")
		if got, err := mac.Sync(SyncOptions{}); err != nil || got.Pushed || got.Behind == "" {
			t.Errorf("sync from an older commit: %+v, %v", got, err)
		}
		if now := gitIn(t, repo, "rev-parse", "origin/main"); now != sent {
			t.Errorf("origin moved from %s to %s", sent, now)
		}
	})
	t.Run("stash on a branch with the operator's commits", func(t *testing.T) {
		_, hosts := notes(t, "mac", "box")
		mac, box := hosts["mac"], hosts["box"]
		repo := filepath.Dir(mac.Dir)
		x := newPlan(t, mac, "x")
		addItems(t, x.file(""), "** Steps", "S1")
		if got, err := mac.Sync(SyncOptions{}); err != nil || !got.Pushed {
			t.Fatalf("mac's sync: %+v, %v", got, err)
		}
		gitIn(t, repo, "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "Operator, unpushed")
		newPlan(t, box, "y")
		if got, err := box.Sync(SyncOptions{}); err != nil || !got.Pushed {
			t.Fatalf("box's sync: %+v, %v", got, err)
		}
		gitIn(t, repo, "fetch", "-q")
		before := gitIn(t, repo, "rev-parse", "origin/main")
		if _, err := x.Log(Event{Kind: "started", Item: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		for _, step := range []string{"with the event", "after a stash"} {
			if step == "after a stash" {
				gitIn(t, repo, "stash", "-q")
			}
			if got, err := mac.Sync(SyncOptions{}); err != nil || got.Pushed || got.Behind == "" {
				t.Errorf("sync %s: %+v, %v", step, got, err)
			}
			if now := gitIn(t, repo, "rev-parse", "origin/main"); now != before {
				t.Errorf("sync %s moved origin from %s to %s", step, before, now)
			}
		}
	})
}

// Sync touches the index only to stage this host's plan files for a
// fast-forward, and then only when nothing else is staged at a plan path
// and no merge, rebase, cherry-pick, revert or bisect is in progress; what
// it stages and cannot fast-forward over, it unstages.
func testSyncLeavesIndex(t *testing.T) {
	// staged gives a plan file of mac's a staged version and an unstaged
	// one on top, and returns its path in the repository.
	staged := func(t *testing.T, mac Config, x Plan) string {
		t.Helper()
		f := x.file("")
		data, _ := os.ReadFile(f)
		os.WriteFile(f, append(data, "staged A\n"...), 0o644)
		gitIn(t, filepath.Dir(mac.Dir), "add", f)
		os.WriteFile(f, append(data, "staged A\nunstaged B\n"...), 0o644)
		return "agents/" + filepath.Base(f)
	}
	for _, collide := range []bool{false, true} {
		t.Run(map[bool]string{false: "a staged version", true: "a staged version and a fast-forward git refuses"}[collide], func(t *testing.T) {
			_, hosts := notes(t, "mac", "box")
			mac, box := hosts["mac"], hosts["box"]
			repo := filepath.Dir(mac.Dir)
			x := newPlan(t, mac, "x")
			if got, err := mac.Sync(SyncOptions{}); err != nil || !got.Pushed {
				t.Fatalf("mac's sync: %+v, %v", got, err)
			}
			y := newPlan(t, box, "y")
			if got, err := box.Sync(SyncOptions{}); err != nil || !got.Pushed {
				t.Fatalf("box's sync: %+v, %v", got, err)
			}
			if collide {
				os.WriteFile(filepath.Join(mac.Dir, filepath.Base(y.file(""))), []byte("the operator's own\n"), 0o644)
			}
			rel := staged(t, mac, x)
			before := gitIn(t, repo, "show", ":"+rel)
			got, err := mac.Sync(SyncOptions{PullOnly: true})
			if err != nil || got.Behind == "" {
				t.Errorf("pull: %+v, %v", got, err)
			}
			if after := gitIn(t, repo, "show", ":"+rel); after != before {
				t.Errorf("the staged version is now:\n%s", after)
			}
		})
	}
	t.Run("a merge in progress", func(t *testing.T) {
		_, hosts := notes(t, "mac", "box")
		mac, box := hosts["mac"], hosts["box"]
		repo := filepath.Dir(mac.Dir)
		x := newPlan(t, mac, "x")
		if got, err := mac.Sync(SyncOptions{}); err != nil || !got.Pushed {
			t.Fatalf("mac's sync: %+v, %v", got, err)
		}
		f := x.file("")
		rel := "agents/" + filepath.Base(f)
		data, _ := os.ReadFile(f)
		gitIn(t, repo, "switch", "-q", "-c", "side")
		os.WriteFile(f, append(data, "side\n"...), 0o644)
		gitIn(t, repo, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "Side", rel)
		gitIn(t, repo, "switch", "-q", "main")
		gitIn(t, repo, "-c", "commit.gpgsign=false", "merge", "-q", "--no-ff", "--no-commit", "side")
		before := gitIn(t, repo, "diff", "--cached", "--name-only")
		newPlan(t, box, "y")
		if got, err := box.Sync(SyncOptions{}); err != nil || !got.Pushed {
			t.Fatalf("box's sync: %+v, %v", got, err)
		}
		got, err := mac.Sync(SyncOptions{PullOnly: true})
		if err != nil || got.Behind == "" {
			t.Errorf("pull: %+v, %v", got, err)
		}
		if after := gitIn(t, repo, "diff", "--cached", "--name-only"); after != before {
			t.Errorf("staged during the merge: %q, before the pull %q", after, before)
		}
		gitIn(t, repo, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	})
	t.Run("a fast-forward refused after staging", func(t *testing.T) {
		_, hosts := notes(t, "mac")
		mac := hosts["mac"]
		repo := filepath.Dir(mac.Dir)
		newPlan(t, mac, "x")
		// The operator commits through an index of their own just as
		// sync fast-forwards to what it sent.
		ran := raceAt(t, repo, "merge", "git update-ref HEAD $(git commit-tree HEAD^{tree} -p HEAD -m 'Racing operator commit')")
		got, err := mac.Sync(SyncOptions{})
		raced(t, ran)
		if err != nil || !got.Pushed || got.Behind == "" {
			t.Errorf("sync: %+v, %v", got, err)
		}
		if staged := gitIn(t, repo, "diff", "--cached", "--name-only"); staged != "" {
			t.Errorf("sync left staged:\n%s", staged)
		}
	})
}

// Sync's push never overwrites what another host pushed after sync fetched.
func testSyncDoesNotForce(t *testing.T) {
	_, hosts := notes(t, "mac", "box")
	mac, box := hosts["mac"], hosts["box"]
	repo, other := filepath.Dir(mac.Dir), filepath.Dir(box.Dir)
	newPlan(t, mac, "x")
	ran := raceAt(t, repo, "push", "git -C '"+other+"' -c commit.gpgsign=false commit -q --allow-empty -m Moved && git -C '"+other+"' push -q origin HEAD:main")
	got, err := mac.Sync(SyncOptions{})
	raced(t, ran)
	if err == nil || got.Pushed {
		t.Errorf("sync after origin moved: %+v, %v", got, err)
	}
	gitIn(t, other, "fetch", "-q")
	if log := gitIn(t, other, "log", "-1", "--format=%s", "origin/main"); log != "Moved\n" {
		t.Errorf("origin's tip is %q, not the other host's commit", log)
	}
}

// A commit the operator makes while sync runs stays theirs when the remote
// has changed a file the operator has uncommitted edits in, and the edits
// stay.
func testSyncKeepsEditsAndRacingCommit(t *testing.T) {
	_, hosts := notes(t, "mac")
	mac := hosts["mac"]
	repo := filepath.Dir(mac.Dir)
	root := filepath.Dir(repo)
	other := filepath.Join(root, "other")
	gitIn(t, root, "clone", "-q", filepath.Join(root, "origin.git"), other)
	os.WriteFile(filepath.Join(other, "todo.org"), []byte("1\n2\n3\n4\n5\n6\n"), 0o644)
	gitIn(t, other, "-c", "commit.gpgsign=false", "commit", "-qam", "Lines")
	gitIn(t, other, "push", "-q", "origin", "main")
	gitIn(t, repo, "checkout", "--", "todo.org")
	gitIn(t, repo, "pull", "-q", "--ff-only")
	const edited = "1 mac\n2\n3\n4\n5\n6\n"
	os.WriteFile(filepath.Join(repo, "todo.org"), []byte(edited), 0o644)
	os.WriteFile(filepath.Join(other, "todo.org"), []byte("1\n2\n3\n4\n5\n6 other\n"), 0o644)
	gitIn(t, other, "-c", "commit.gpgsign=false", "commit", "-qam", "Other edits todo")
	gitIn(t, other, "push", "-q", "origin", "main")
	newPlan(t, mac, "x")
	ran := raceAt(t, repo, "merge-base", "git -c commit.gpgsign=false commit -q --allow-empty -m 'Racing operator commit'")
	got, err := mac.Sync(SyncOptions{})
	raced(t, ran)
	t.Logf("sync: %+v, %v", got, err)
	operatorsCommit(t, repo, "Racing operator commit")
	if data, _ := os.ReadFile(filepath.Join(repo, "todo.org")); string(data) != edited {
		t.Errorf("the operator's edits to todo.org are now %q", data)
	}
}

// A pull-only sync moves no branch the operator switches to while it runs.
func testSyncLeavesSwitchedBranch(t *testing.T) {
	_, hosts := notes(t, "mac", "box")
	mac, box := hosts["mac"], hosts["box"]
	repo := filepath.Dir(mac.Dir)
	// An old ORIG_HEAD, from a reset the operator once made.
	start := strings.TrimSpace(gitIn(t, repo, "rev-parse", "HEAD"))
	gitIn(t, repo, "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "Undone")
	gitIn(t, repo, "reset", "-q", "--soft", start)
	newPlan(t, box, "y")
	if got, err := box.Sync(SyncOptions{}); err != nil || !got.Pushed {
		t.Fatalf("box's sync: %+v, %v", got, err)
	}
	gitIn(t, repo, "fetch", "-q")
	gitIn(t, repo, "branch", "work", "origin/main")
	work := strings.TrimSpace(gitIn(t, repo, "rev-parse", "work"))
	ran := raceAt(t, repo, "merge-base", "git switch -q work")
	got, err := mac.Sync(SyncOptions{PullOnly: true})
	raced(t, ran)
	t.Logf("sync: %+v, %v", got, err)
	if now := strings.TrimSpace(gitIn(t, repo, "rev-parse", "work")); now != work {
		t.Errorf("sync moved the operator's branch work from %s to %s", work, now)
	}
}

// guarded is notes whose hosts reach origin as a host reaches the notes
// repository, at git@github-notes:wesnel/notes.git over ssh, under a global
// config that, as home-manager's does, names the operator in user.email and
// runs git-push-guard, built from this module, before every push. The
// operator's identity comes from that config alone, as on a host. The ssh is
// a fake that runs git's command against the bare origin on this machine: it
// stands in for GitHub, which a test must not contact.
func guarded(t *testing.T, hosts ...string) (url string, configs map[string]Config) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go here to build git-push-guard")
	}
	origin, configs := notes(t, hosts...)
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		os.Unsetenv(k)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	guard := filepath.Join(dir, "git-push-guard")
	if out, err := exec.Command("go", "build", "-o", guard, "github.com/wesnel/agent-tools/cmd/git-push-guard").CombinedOutput(); err != nil {
		t.Fatalf("building git-push-guard: %v\n%s", err, out)
	}
	os.MkdirAll(filepath.Join(dir, "wesnel"), 0o755)
	if err := os.Symlink(origin, filepath.Join(dir, "wesnel", "notes.git")); err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(dir, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\ncd '"+dir+"' && exec sh -c \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", ssh)
	t.Setenv("GIT_SSH_VARIANT", "simple")
	global := filepath.Join(dir, "gitconfig")
	config := "[user]\n\tname = Operator\n\temail = operator@example.com\n" +
		"[hook \"push-guard\"]\n\tevent = pre-push\n\tcommand = " + guard + "\n"
	if err := os.WriteFile(global, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	url = "git@github-notes:wesnel/notes.git"
	for _, c := range configs {
		gitIn(t, filepath.Dir(c.Dir), "remote", "set-url", "origin", url)
	}
	return url, configs
}

// Sync's pushes pass git-push-guard, which refuses the operator's own
// unsigned commits, with no bypass: box's first sync, and mac's, whose
// commit is rebased onto box's before it is pushed.
func testSyncThroughPushGuard(t *testing.T) {
	url, hosts := guarded(t, "mac", "box")
	mac, box := hosts["mac"], hosts["box"]
	// The guard is running: it refuses the operator's own unsigned commit.
	control := filepath.Join(t.TempDir(), "control")
	gitIn(t, filepath.Dir(control), "clone", "-q", url, control)
	gitIn(t, control, "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "Unsigned")
	push := exec.Command("git", "push", "-q", "origin", "HEAD:refs/heads/control")
	push.Dir, push.Env = control, gitenv.Without(os.Environ())
	if out, err := push.CombinedOutput(); err == nil || !strings.Contains(string(out), "are not signed") {
		t.Fatalf("git-push-guard did not refuse the operator's unsigned commit: %v\n%s", err, out)
	}

	newPlan(t, box, "y")
	if got, err := box.Sync(SyncOptions{}); err != nil || !got.Pushed {
		t.Fatalf("box's sync: %+v, %v", got, err)
	}
	newPlan(t, mac, "x")
	if got, err := mac.Sync(SyncOptions{}); err != nil || !got.Pushed {
		t.Fatalf("mac's sync, rebased onto box's: %+v, %v", got, err)
	}
	untouched(t, mac)
	log := gitIn(t, filepath.Dir(mac.Dir), "log", "--format=%an <%ae> %cn <%ce> %s", "origin/main")
	want := "agent-plan sync <agent-plan@mac.invalid> agent-plan sync <agent-plan@mac.invalid> Sync plans from mac\n" +
		"agent-plan sync <agent-plan@box.invalid> agent-plan sync <agent-plan@box.invalid> Sync plans from box\n" +
		"Operator <operator@example.com> Operator <operator@example.com> Start\n"
	if log != want {
		t.Errorf("origin's history:\n%s\nnot\n%s", log, want)
	}
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
	if !got.Pushed || len(got.Committed) != 2 || got.Behind != "" {
		t.Errorf("mac's sync: %+v", got)
	}
	atOrigin(t, mac)
	untouched(t, mac)
	theirs := newPlan(t, box, "y")
	if got, err := box.Sync(SyncOptions{}); err != nil || !got.Pushed {
		t.Fatalf("box's sync: %+v, %v", got, err)
	}
	atOrigin(t, box)
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
	if log != "agent-plan sync N Sync plans from box\nagent-plan sync N Sync plans from mac\nOperator N Start\n" {
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
	// A plan changed since it was last sent, and so modified against HEAD.
	if _, err := mine.Log(Event{Kind: "done", Item: "S1"}, day); err != nil {
		t.Fatal(err)
	}
	if got, err := mac.Sync(SyncOptions{}); err != nil || !got.Pushed || len(got.Committed) != 2 {
		t.Errorf("mac's second sync: %+v, %v", got, err)
	}
	atOrigin(t, mac)
	untouched(t, mac)
}

// atOrigin checks that a host's branch was brought forward to origin's,
// over the plan files the host had just sent, uncommitted, and that none
// is left modified.
func atOrigin(t *testing.T, c Config) {
	t.Helper()
	repo := filepath.Dir(c.Dir)
	if head, origin := gitIn(t, repo, "rev-parse", "HEAD"), gitIn(t, repo, "rev-parse", "origin/main"); head != origin {
		t.Errorf("%s: HEAD %s is not origin's %s", c.Host, strings.TrimSpace(head), strings.TrimSpace(origin))
	}
	if status := gitIn(t, repo, "status", "--porcelain", "--", "agents"); status != "" {
		t.Errorf("%s: plan files left changed:\n%s", c.Host, status)
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

// Find, status and new pull first, quietly, so another host's plans are
// current; a pull that cannot run changes nothing.
func TestPull(t *testing.T) {
	t.Run("takes the other hosts' plans", testPullTakes)
	t.Run("leaves unsent edits unstaged", testPullLeavesEditsUnstaged)
}

func testPullTakes(t *testing.T) {
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

// A pull that fast-forwards over this host's plan edits not yet sent leaves
// them as it found them, unstaged.
func testPullLeavesEditsUnstaged(t *testing.T) {
	_, hosts := notes(t, "mac", "box")
	mac, box := hosts["mac"], hosts["box"]
	repo := filepath.Dir(mac.Dir)
	x := newPlan(t, mac, "x")
	if got, err := mac.Sync(SyncOptions{}); err != nil || !got.Pushed {
		t.Fatalf("mac's sync: %+v, %v", got, err)
	}
	addItems(t, x.file(""), "** Steps", "S1")
	newPlan(t, box, "y")
	if got, err := box.Sync(SyncOptions{}); err != nil || !got.Pushed {
		t.Fatalf("box's sync: %+v, %v", got, err)
	}
	mac.Pull()
	if head, origin := gitIn(t, repo, "rev-parse", "HEAD"), gitIn(t, repo, "rev-parse", "origin/main"); head != origin {
		t.Errorf("the pull did not fast-forward: HEAD %s, origin %s", head, origin)
	}
	if staged := gitIn(t, repo, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("the pull left staged:\n%s", staged)
	}
	if changed := gitIn(t, repo, "diff", "--name-only", "--", "agents"); changed != "agents/"+filepath.Base(x.file(""))+"\n" {
		t.Errorf("the unsent edit is not left unstaged: %q", changed)
	}
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
