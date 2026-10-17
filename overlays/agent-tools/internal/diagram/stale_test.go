package diagram

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "T", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "T",
		"GIT_COMMITTER_EMAIL": "t@example.com"} {
		t.Setenv(k, v)
	}
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = gitenv.Without(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files and commits them, returning the commit.
func (r *repo) commit(files map[string]string) string {
	r.t.Helper()
	for path, text := range files {
		full := filepath.Join(r.dir, path)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "c")
	return r.git("rev-parse", "HEAD")
}

const queueGo = `package queue

const (
	Queued = "queued"
	Ready  = "ready"
)

func other() {}
`

// checked is a diagram of queueGo at commit c, checked there.
func checked(t *testing.T, r *repo, c string) *Diagram {
	t.Helper()
	d := diagramOf(t, "state", "N1 queue.go:4 Queued", "N2 queue.go:5 Ready")
	d.At = c
	g := GitRepo(r.dir)
	res := Check(d, func(path string) ([]byte, error) { return g.Blob(c, path) }, nil)
	if len(res.Failures) != 0 {
		t.Fatal(res.Failures)
	}
	return d
}

func states(s Staleness) []string {
	var out []string
	for _, e := range s.Elements {
		out = append(out, e.ID+":"+string(e.State))
	}
	return out
}

func TestStale(t *testing.T) {
	t.Run("unchanged", func(t *testing.T) {
		r := newRepo(t)
		c := r.commit(map[string]string{"queue.go": queueGo})
		d := checked(t, r, c)
		s, err := Stale(d, c, GitRepo(r.dir))
		if err != nil || s.Stale || s.Updated != nil || !slices.Equal(states(s), []string{"N1:current", "N2:current"}) {
			t.Errorf("%+v %v", s, err)
		}
	})

	// Lines moved by an edit above them, reindented, are the same code:
	// the citation follows them, and the diagram moves to the new commit.
	t.Run("moved", func(t *testing.T) {
		r := newRepo(t)
		c := r.commit(map[string]string{"queue.go": queueGo})
		d := checked(t, r, c)
		now := r.commit(map[string]string{"queue.go": "package queue\n\n// States.\nconst (\n\t\tQueued = \"queued\"\n\tReady  = \"ready\"\n)\n\nfunc other() {}\n"})
		s, err := Stale(d, now, GitRepo(r.dir))
		if err != nil || s.Stale || !slices.Equal(states(s), []string{"N1:moved", "N2:moved"}) || s.Updated == nil {
			t.Fatalf("%+v %v", s, err)
		}
		if u := s.Updated; u.At != now || u.Nodes[0].Cite.Line != 5 || u.Nodes[1].Cite.Line != 6 || d.Nodes[0].Cite.Line != 4 {
			t.Errorf("updated %+v, from %+v", u.Nodes, d.Nodes)
		}
	})

	t.Run("renamed", func(t *testing.T) {
		r := newRepo(t)
		c := r.commit(map[string]string{"queue.go": queueGo})
		d := checked(t, r, c)
		r.git("mv", "queue.go", "states.go")
		r.git("commit", "-q", "-m", "rename")
		now := r.git("rev-parse", "HEAD")
		s, err := Stale(d, now, GitRepo(r.dir))
		if err != nil || s.Stale || s.Updated == nil || s.Updated.Nodes[0].Cite.Path != "states.go" ||
			!slices.Equal(states(s), []string{"N1:moved", "N2:moved"}) {
			t.Errorf("%+v %v", s, err)
		}
	})

	t.Run("changed", func(t *testing.T) {
		r := newRepo(t)
		c := r.commit(map[string]string{"queue.go": queueGo})
		d := checked(t, r, c)
		now := r.commit(map[string]string{"queue.go": strings.Replace(queueGo, `"ready"`, `"done"`, 1)})
		s, err := Stale(d, now, GitRepo(r.dir))
		if err != nil || !s.Stale || s.Updated != nil || !slices.Equal(states(s), []string{"N1:current", "N2:stale"}) {
			t.Errorf("%+v %v", s, err)
		}
	})

	t.Run("gone", func(t *testing.T) {
		r := newRepo(t)
		c := r.commit(map[string]string{"queue.go": queueGo, "keep.txt": "x\n"})
		d := checked(t, r, c)
		r.git("rm", "-q", "queue.go")
		r.git("commit", "-q", "-m", "rm")
		s, err := Stale(d, r.git("rev-parse", "HEAD"), GitRepo(r.dir))
		if err != nil || !s.Stale || !slices.Equal(states(s), []string{"N1:stale", "N2:stale"}) {
			t.Errorf("%+v %v", s, err)
		}
	})

	// A cited file changed away from the cited lines changes no citation,
	// but may hold a new call or state the diagram lacks.
	t.Run("changed elsewhere", func(t *testing.T) {
		r := newRepo(t)
		c := r.commit(map[string]string{"queue.go": queueGo})
		d := checked(t, r, c)
		now := r.commit(map[string]string{"queue.go": queueGo + "\nfunc more() { other() }\n"})
		s, err := Stale(d, now, GitRepo(r.dir))
		if err != nil || s.Stale || s.Updated != nil || !slices.Equal(s.Incomplete, []string{"queue.go"}) {
			t.Errorf("%+v %v", s, err)
		}
	})
}

// twoAssigns has the same transition line in a() and b(); the diagram
// cites a()'s.
const twoAssigns = `package queue

const Ready = "ready"

type Entry struct{ State string }

func b(e *Entry) {
	e.State = Ready
}

func a(e *Entry) {
	e.State = Ready
}
`

func transition(t *testing.T, r *repo, c string) *Diagram {
	t.Helper()
	d := diagramOf(t, "state", "N1 queue.go:3 Ready", "N2 queue.go:3 Ready", "E1:N1>N2 queue.go:12 Ready")
	d.At = c
	g := GitRepo(r.dir)
	if res := Check(d, func(path string) ([]byte, error) { return g.Blob(c, path) }, nil); len(res.Failures) != 0 {
		t.Fatal(res.Failures)
	}
	return d
}

// A citation follows its own lines, as git's diff maps them, never another
// copy of the same text.
func TestStaleFollowsItsOwnLines(t *testing.T) {
	t.Run("lines added above", func(t *testing.T) {
		r := newRepo(t)
		c := r.commit(map[string]string{"queue.go": twoAssigns})
		d := transition(t, r, c)
		now := r.commit(map[string]string{"queue.go": strings.Replace(twoAssigns, "func a(e *Entry) {\n",
			"func a(e *Entry) {\n"+strings.Repeat("\t_ = 1\n", 10), 1)})
		s, err := Stale(d, now, GitRepo(r.dir))
		if err != nil || s.Stale || s.Updated == nil || s.Updated.Edges[0].Cite.Line != 22 {
			t.Fatalf("%+v %v", s, err)
		}
	})
	t.Run("its lines deleted, a copy left", func(t *testing.T) {
		r := newRepo(t)
		c := r.commit(map[string]string{"queue.go": twoAssigns})
		d := transition(t, r, c)
		now := r.commit(map[string]string{"queue.go": strings.Replace(twoAssigns, "func a(e *Entry) {\n\te.State = Ready\n",
			"func a(e *Entry) {\n", 1)})
		s, err := Stale(d, now, GitRepo(r.dir))
		if err != nil || !s.Stale || s.Updated != nil || !slices.Contains(states(s), "E1:stale") {
			t.Fatalf("%+v %v", s, err)
		}
	})
	// Moved lines that no longer pass the check, such as a declaration now
	// inside another, count as stale.
	t.Run("moved but failing its check", func(t *testing.T) {
		r := newRepo(t)
		c := r.commit(map[string]string{"queue.go": "package queue\n\nfunc Run() {\n\thelper()\n}\n\nfunc helper() {}\n"})
		d := diagramOf(t, "callflow", "N1 queue.go:3 Run", "N2 queue.go:7 helper", "E1:N1>N2 queue.go:4 helper")
		d.At = c
		g := GitRepo(r.dir)
		if res := Check(d, func(path string) ([]byte, error) { return g.Blob(c, path) }, nil); len(res.Failures) != 0 {
			t.Fatal(res.Failures)
		}
		now := r.commit(map[string]string{"queue.go": "package queue\n\nfunc Run() {\n}\n\nfunc other() {\n\thelper()\n}\n\nfunc helper() {}\n"})
		s, err := Stale(d, now, GitRepo(r.dir))
		if err != nil || !s.Stale || s.Updated != nil {
			t.Fatalf("%+v %v", s, err)
		}
	})
}

// A repository is named by its root commit, the same in every clone.
func TestRoot(t *testing.T) {
	r := newRepo(t)
	first := r.commit(map[string]string{"a.txt": "a\n"})
	now := r.commit(map[string]string{"a.txt": "b\n"})
	if got, err := GitRepo(r.dir).Root(now); err != nil || got != first {
		t.Errorf("root %q %v, want %s", got, err, first)
	}
}

// A TimedRepo's git calls stop with its context.
func TestTimedRepoStops(t *testing.T) {
	r := newRepo(t)
	c := r.commit(map[string]string{"a.txt": "a\n"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (TimedRepo{Dir: r.dir, Ctx: ctx}).Blob(c, "a.txt"); err == nil {
		t.Error("a cancelled call read the blob")
	}
}

// A blob past the cap is never read whole.
func TestBlobCap(t *testing.T) {
	r := newRepo(t)
	c := r.commit(map[string]string{"big.txt": strings.Repeat("y", MaxBlob+1)})
	if _, err := GitRepo(r.dir).Blob(c, "big.txt"); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("%v", err)
	}
}
