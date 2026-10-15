package loop

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// githubSSH writes a fake OpenSSH that serves git@github.com from the bare
// repository dir/o/r.git, and shares connections as OpenSSH does: a login
// authenticates, which is a key touch, unless a master is up on its
// ControlPath, and one started with ControlMaster=auto and a ControlPersist
// stays up until `-O exit`. It logs "auth", "open" and "close" to the log
// it returns, and core.sshCommand in global names it. It is a fake because a
// test must not reach GitHub or touch a key. The ssh it puts first on PATH
// only fails, so a push that loses the configured command fails instead of
// reaching the real one.
func githubSSH(t *testing.T, dir, global string) (log string) {
	t.Helper()
	program, log := filepath.Join(dir, "github-ssh"), filepath.Join(dir, "ssh.log")
	script := `#!/bin/sh
path= master= persist= op=
while [ $# -gt 0 ]; do
  case $1 in
  -o) shift
    case $1 in
    ControlPath=*) path=${1#ControlPath=} ;;
    ControlMaster=*) master=${1#ControlMaster=} ;;
    ControlPersist=*) persist=${1#ControlPersist=} ;;
    esac ;;
  -O) shift; op=$1 ;;
  -p) shift ;;
  -*) ;;
  *) break ;;
  esac
  shift
done
case $path in *%C) path="${path%??}$1" ;; esac
if [ "$op" = exit ]; then
  [ -n "$path" ] && [ -e "$path" ] || exit 255
  rm "$path"
  echo close >> 'LOG'
  exit 0
fi
[ "$1" = git@github.com ] || { echo "no host $1" >&2; exit 255; }
if [ -z "$path" ] || [ ! -e "$path" ]; then
  echo auth >> 'LOG'
  if [ -n "$path" ] && [ "$master" = auto ] && [ -n "$persist" ] && [ "$persist" != no ]; then
    : > "$path"
    echo open >> 'LOG'
  fi
fi
# A remote's git never sees the client's -c settings.
unset GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT
cd 'DIR' && exec sh -c "$2"
`
	script = strings.NewReplacer("LOG", log, "DIR", dir).Replace(script)
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	os.Mkdir(bin, 0o755)
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\necho 'the ssh on PATH ran' >&2\nexit 255\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_SSH_COMMAND", "")
	os.Unsetenv("GIT_SSH_COMMAND")
	t.Setenv("GIT_SSH_VARIANT", "ssh")
	gitIn(t, dir, "config", "--file", global, "core.sshCommand", program)
	return log
}

// readLog returns a log's lines since the last call, and empties it.
func readLog(t *testing.T, log string) string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	os.Remove(log)
	return strings.TrimSpace(string(data))
}

// signStack is newFixedStack signed with a throwaway SSH key, its loop
// registered for o/r with PR 1 (lower) and PR 2 (upper), and GitHub's copy,
// reached over the fake githubSSH, holding both branches as they stand. Its
// post-receive hook logs each push as one line naming the branches it
// moved, and while dir/reject exists it refuses upper.
func signStack(t *testing.T) (s *fixedStack, bare, pushes, sshLog string) {
	t.Helper()
	s = newFixedStack(t)
	keys := t.TempDir()
	key := filepath.Join(keys, "key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Skipf("no ssh-keygen: %v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(keys, "signers")
	os.WriteFile(signers, []byte("t@e namespaces=\"git\" "+string(pub)), 0o644)
	global := filepath.Join(keys, "gitconfig")
	os.WriteFile(global, []byte("[user]\n\tname = T\n\temail = t@e\n\tsigningkey = "+key+
		"\n[gpg]\n\tformat = ssh\n[gpg \"ssh\"]\n\tallowedSignersFile = "+signers+"\n"), 0o644)

	remote := t.TempDir()
	sshLog = githubSSH(t, remote, global)
	bare, pushes = filepath.Join(remote, "o", "r.git"), filepath.Join(remote, "pushes")
	gitIn(t, remote, "init", "-q", "--bare", bare)
	hooks := map[string]string{
		"post-receive": "#!/bin/sh\ncut -d' ' -f3 | sed 's,^refs/heads/,,' | paste -sd' ' - >> '" + pushes + "'\n",
		"pre-receive": "#!/bin/sh\nwhile read old new ref; do\n  if [ \"$ref\" = refs/heads/upper ] && [ -e '" +
			filepath.Join(remote, "reject") + "' ]; then echo 'upper is refused here' >&2; exit 1; fi\ndone\n",
	}
	for name, hook := range hooks {
		if err := os.WriteFile(filepath.Join(bare, "hooks", name), []byte(hook), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, s.dir, "push", "-q", bare, "main", "lower", "upper")
	readLog(t, pushes)
	gitIn(t, s.dir, "remote", "add", "origin", "https://github.com/o/r.git")
	gitIn(t, s.dir, "update-ref", "refs/remotes/origin/main", "main")
	s.state.Repo = "o/r"
	for i, b := range s.state.Order {
		s.state.Branches[b].PR = i + 1
		s.state.Branches[b].RemoteOid = gitIn(t, s.dir, "rev-parse", b)
	}
	if err := s.state.save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_EDITOR", ":")
	old := graphQL
	graphQL = (&fakeGitHub{}).graphQL
	t.Cleanup(func() { graphQL = old })
	return s, bare, pushes, sshLog
}

// TestSign checks that pr-loop sign pushes the signed stack one branch per
// push, bottom up, as GitHub needs to start each pull request's workflows
// once, over a single SSH login, one key touch; and that a push refused
// partway carries on from the branch refused.
func TestSign(t *testing.T) {
	t.Run("one branch per push, one login", func(t *testing.T) {
		s, bare, pushes, sshLog := signStack(t)
		if _, err := Sign(false, false); err != nil {
			t.Fatal(err)
		}
		if got := readLog(t, pushes); got != "lower\nupper" {
			t.Errorf("GitHub took these pushes:\n%s", got)
		}
		if got := readLog(t, sshLog); got != "auth\nopen\nclose" {
			t.Errorf("SSH logins and shared connections:\n%s", got)
		}
		for _, b := range []string{"lower", "upper"} {
			if got, want := gitIn(t, bare, "rev-parse", b), gitIn(t, s.dir, "rev-parse", b); got != want {
				t.Errorf("GitHub has %s at %s, want %s", b, got, want)
			}
		}
	})

	t.Run("refused partway", func(t *testing.T) {
		s, bare, pushes, sshLog := signStack(t)
		before := gitIn(t, bare, "rev-parse", "upper")
		reject := filepath.Join(filepath.Dir(filepath.Dir(bare)), "reject")
		os.WriteFile(reject, nil, 0o644)
		if _, err := Sign(false, false); err == nil || !strings.Contains(err.Error(), "pr-loop sign --resume") {
			t.Fatalf("a push GitHub refuses: %v", err)
		}
		if got := readLog(t, pushes); got != "lower" {
			t.Errorf("GitHub took these pushes:\n%s", got)
		}
		if got, want := gitIn(t, bare, "rev-parse", "lower"), gitIn(t, s.dir, "rev-parse", "lower"); got != want {
			t.Errorf("GitHub has lower at %s, want %s", got, want)
		}
		if got := gitIn(t, bare, "rev-parse", "upper"); got != before {
			t.Errorf("GitHub has upper at %s, want it unmoved at %s", got, before)
		}
		readLog(t, sshLog)

		os.Remove(reject)
		if _, err := Sign(true, false); err != nil {
			t.Fatalf("resuming: %v", err)
		}
		if got := readLog(t, pushes); got != "upper" {
			t.Errorf("the resumed push took these:\n%s", got)
		}
		if got := readLog(t, sshLog); got != "auth\nopen\nclose" {
			t.Errorf("SSH logins and shared connections on resuming:\n%s", got)
		}
		if got, want := gitIn(t, bare, "rev-parse", "upper"), gitIn(t, s.dir, "rev-parse", "upper"); got != want {
			t.Errorf("GitHub has upper at %s, want %s", got, want)
		}
	})
}
