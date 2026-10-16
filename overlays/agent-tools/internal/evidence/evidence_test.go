package evidence

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/hostpaths"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

func exit(n int) *int { return &n }

// A record is written redacted, its output cut to its last lines, into a
// file only the user can read, and read back by session.
func TestAppendRead(t *testing.T) {
	state := t.TempDir()
	hostpaths.Bake(t, t.TempDir(), state)
	token := "ghp_" + strings.Repeat("a", 36)
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	lines = append(lines, "GITHUB_TOKEN="+token)
	now := time.Now()
	r := Record{Time: now, Session: "s-1", AgentID: "a1", Cwd: "/w", Head: "h", Tree: "t",
		Command: "curl -H 'Authorization: token " + token + "' x", Exit: exit(3), OutputTail: strings.Join(lines, "\n") + "\n"}
	if err := Append(r); err != nil {
		t.Fatal(err)
	}
	if err := Append(Record{Time: now, Session: "s-2", Command: "true", Exit: exit(0)}); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(state, "agent-tools", "evidence"); Dir() != want {
		t.Errorf("Dir() = %q, want %q", Dir(), want)
	}
	log, _ := file("s-1", now)
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token) {
		t.Errorf("the log holds the token: %s", data)
	}
	for path, mode := range map[string]os.FileMode{log: 0o600, Dir(): 0o700} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: %v, mode %v, want %v", path, err, info.Mode().Perm(), mode)
		}
	}
	got, err := Read([]string{"s-1"}, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("Read: %v, %d records", err, len(got))
	}
	tail := strings.Split(strings.TrimSuffix(got[0].OutputTail, "\n"), "\n")
	if len(tail) != TailLines || tail[0] != "line 62" || tail[len(tail)-1] != "GITHUB_TOKEN=[REDACTED]" {
		t.Errorf("tail is %d lines, %q to %q", len(tail), tail[0], tail[len(tail)-1])
	}
	if got[0].Exit == nil || *got[0].Exit != 3 || got[0].Head != "h" || got[0].Tree != "t" || got[0].AgentID != "a1" ||
		!strings.Contains(got[0].Command, "[REDACTED]") {
		t.Errorf("read back %+v", got[0])
	}
	both, _ := Read([]string{"s-1", "s-2", "none"}, now)
	if len(both) != 2 {
		t.Errorf("two sessions read %d records", len(both))
	}
}

// Logs and records older than Keep are gone; a session that cannot name a
// file is refused.
func TestKeep(t *testing.T) {
	hostpaths.Bake(t, t.TempDir(), t.TempDir())
	now := time.Now()
	old := now.Add(-Keep - time.Hour)
	if err := Append(Record{Time: old, Session: "old", Command: "true"}); err != nil {
		t.Fatal(err)
	}
	if err := Append(Record{Time: old, Session: "live", Command: "early"}); err != nil {
		t.Fatal(err)
	}
	// A record of a day that ended longer than Keep ago, in a session's
	// log that is still written.
	if err := Append(Record{Time: now.Add(-Keep - 25*time.Hour), Session: "live", Command: "earliest"}); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(Dir(), "old.jsonl")
	os.WriteFile(stale, []byte("{}\n"), 0o600)
	os.Chtimes(stale, old, old)
	if err := Append(Record{Time: now, Session: "live", Command: "late"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a log untouched for longer than Keep is still there: %v", err)
	}
	got, _ := Read([]string{"live"}, now)
	if len(got) != 1 || got[0].Command != "late" {
		t.Errorf("read %+v, want the late record alone", got)
	}
	entries, _ := os.ReadDir(Dir())
	for _, e := range entries {
		if data, _ := os.ReadFile(filepath.Join(Dir(), e.Name())); strings.Contains(string(data), "earliest") {
			t.Errorf("%s still holds a record of a day that ended longer than Keep ago", e.Name())
		}
	}
	for _, s := range []string{"", "../x", "a/b", ".hidden"} {
		if err := Append(Record{Time: now, Session: s, Command: "x"}); err == nil {
			t.Errorf("session %q was taken", s)
		}
	}
}

// A record holds a bounded command and output, each cut marked, and a log
// line longer than any record, as an older writer could leave, does not
// fail the read.
func TestRecordSize(t *testing.T) {
	hostpaths.Bake(t, t.TempDir(), t.TempDir())
	now := time.Now()
	huge := strings.Repeat("x", 5<<20)
	if err := Append(Record{Time: now, Session: "big", Command: "cat <<EOF\n" + huge + "\nEOF",
		OutputTail: huge + "\n" + huge}); err != nil {
		t.Fatal(err)
	}
	big, _ := file("big", now)
	if info, err := os.Stat(big); err != nil || info.Size() > maxRecord {
		t.Errorf("a record of %v bytes, want at most %d", info.Size(), maxRecord)
	}
	// Control characters take six bytes each in JSON.
	if err := Append(Record{Time: now, Session: "ctl", Command: "x",
		OutputTail: strings.Repeat(strings.Repeat("\x01", maxLine)+"\n", TailLines)}); err != nil {
		t.Fatal(err)
	}
	ctl, _ := file("ctl", now)
	if info, err := os.Stat(ctl); err != nil || info.Size() > maxRecord {
		t.Errorf("a record of control characters of %v bytes, want at most %d", info.Size(), maxRecord)
	}
	f, _ := os.OpenFile(big, os.O_WRONLY|os.O_APPEND, 0)
	fmt.Fprintf(f, `{"time":%q,"session":"big","command":"%s"}`+"\n", now.Format(time.RFC3339Nano), huge)
	f.Close()
	got, err := Read([]string{"big"}, now)
	if err != nil || len(got) != 2 {
		t.Fatalf("Read: %v, %d records", err, len(got))
	}
	if !strings.Contains(got[0].Command, cutMark) || !strings.Contains(got[0].OutputTail, cutMark) {
		t.Errorf("a cut is unmarked: command %d bytes, output %d", len(got[0].Command), len(got[0].OutputTail))
	}
	if !strings.HasPrefix(got[0].Command, "cat <<EOF\nxxx") {
		t.Errorf("the command's start is lost: %.20q", got[0].Command)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The tree is the worktree's files as they are, untracked ones too and
// ignored ones not, and taking it leaves the index alone.
func TestWorktree(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a"), []byte("a\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored\n"), 0o644)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "one")
	head := runGit(t, dir, "rev-parse", "HEAD")
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	gotHead, tree, _ := Worktree(filepath.Join(dir, "sub"))
	if gotHead != head || tree != runGit(t, dir, "rev-parse", "HEAD^{tree}") {
		t.Errorf("clean: %s %s, want %s and HEAD's tree", gotHead, tree, head)
	}
	os.WriteFile(filepath.Join(dir, "ignored"), []byte("x\n"), 0o644)
	if _, again, _ := Worktree(dir); again != tree {
		t.Errorf("an ignored file changed the tree")
	}
	os.WriteFile(filepath.Join(dir, "a"), []byte("b\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "new"), []byte("n\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "large"), make([]byte, maxUntracked+1), 0o644)
	status := runGit(t, dir, "status", "--porcelain")
	objects := runGit(t, dir, "count-objects", "-v")
	_, dirty, skipped := Worktree(dir)
	if after := runGit(t, dir, "status", "--porcelain"); after != status {
		t.Errorf("taking the tree changed the index: %q, was %q", after, status)
	}
	if after := runGit(t, dir, "count-objects", "-v"); after != objects {
		t.Errorf("taking the tree wrote objects into the repository:\n%s\nwas\n%s", after, objects)
	}
	if len(skipped) != 1 || skipped[0] != "large" {
		t.Errorf("skipped %q, want the large untracked file", skipped)
	}
	os.Remove(filepath.Join(dir, "large"))
	runGit(t, dir, "add", "-A")
	if want := runGit(t, dir, "write-tree"); dirty != want {
		t.Errorf("dirty tree %s, want %s", dirty, want)
	}
	if h, tr, _ := Worktree(t.TempDir()); h != "" || tr != "" {
		t.Errorf("outside a repository: %q %q", h, tr)
	}
	// A clone sharing another's objects finds them through its alternates.
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, dir, "clone", "-q", "--shared", dir, clone)
	os.WriteFile(filepath.Join(clone, "a"), []byte("c\n"), 0o644)
	if _, tr, _ := Worktree(clone); tr == "" {
		t.Errorf("no tree in a shared clone")
	}
}
