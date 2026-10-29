package plans

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/refusal"
)

var day = time.Date(2026, 10, 14, 13, 5, 0, 0, time.Local)

func testConfig(t *testing.T) Config {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	return Config{Dir: filepath.Join(dir, "notes", "agents"), Host: "box"}
}

func newPlan(t *testing.T, c Config, slug string) Plan {
	t.Helper()
	made, err := c.New(NewOptions{Title: "Add the loop", Slug: slug, Repos: []string{"o/r"},
		Branches: []string{"main..loop"}, Worktrees: []string{"/w/loop"}, Sessions: []string{"claude:abc"},
		Harness: "claude"}, day)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// addItems puts headings with IDs under a plan file's heading, each in
// CUSTOM_ID as agent-plan writes it.
func addItems(t *testing.T, file, under string, ids ...string) {
	t.Helper()
	addItemsIn(t, "CUSTOM_ID", file, under, ids...)
}

// addItemsIn is addItems with each ID in property, such as ID for a plan
// in the old form.
func addItemsIn(t *testing.T, property, file, under string, ids ...string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "*** TODO Step %s\n:PROPERTIES:\n:%s: %s\n:END:\n", id, property, id)
	}
	text := strings.Replace(string(data), under+"\n", under+"\n"+b.String(), 1)
	if text == string(data) {
		t.Fatalf("%s has no %q", file, under)
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setGoal(t *testing.T, file, goal string) {
	t.Helper()
	data, _ := os.ReadFile(file)
	text := strings.Replace(string(data), "** Goal\n", "** Goal\n"+goal+"\n", 1)
	os.WriteFile(file, []byte(text), 0o644)
}

// eventsBut are p's events but those of kind, such as the property events
// a plan starts with.
func eventsBut(t *testing.T, p Plan, kind string) []Event {
	t.Helper()
	events, err := p.Events()
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range events {
		if e.Kind != kind {
			out = append(out, e)
		}
	}
	return out
}

// isRefusal checks err is a refusal that says what to do instead.
func isRefusal(t *testing.T, what string, err error) {
	t.Helper()
	var r refusal.Refusal
	switch {
	case !errors.As(err, &r):
		t.Errorf("%s: want a refusal, got %v", what, err)
	case !refusal.SaysInstead(r.Error()):
		t.Errorf("%s: %q does not say what to do instead", what, r.Error())
	}
}

// A plan is three kinds of file side by side in the plans directory, named
// for its date, slug and host.
func TestNew(t *testing.T) {
	// A plan is notes on the work, written whether or not the notes
	// repository could be cloned; why it is not synced is a warning.
	t.Run("deferred clone", func(t *testing.T) {
		c, _, key := cloning(t)
		made, err := c.New(NewOptions{Title: "Check", Slug: "check"}, day)
		if err != nil {
			t.Fatalf("missing deploy key refused the plan: %v", err)
		}
		if !strings.Contains(made.Note, key) {
			t.Errorf("warning %q does not name the missing key %s", made.Note, key)
		}
		if filepath.Dir(made.Plan) != c.Dir {
			t.Errorf("plan %s is not in %s", made.Plan, c.Dir)
		}
		if _, err := os.Stat(made.Plan); err != nil {
			t.Errorf("plan not written: %v", err)
		}
		if data, err := os.ReadFile(filepath.Join(c.Dir, ".gitignore")); err != nil || string(data) != "*.lock\n" {
			t.Errorf("plans directory ignore: %q, %v", data, err)
		}
	})

	t.Run("files", func(t *testing.T) {
		c := testConfig(t)
		made, err := c.New(NewOptions{Title: "Add the loop", Slug: "add-loop", Repos: []string{"o/r"},
			Branches: []string{"main..loop"}, Worktrees: []string{"/w/loop"}, Sessions: []string{"claude:abc"},
			Harness: "claude"}, day)
		if err != nil {
			t.Fatal(err)
		}
		// Named the org-roam way, a plan sorts by when it was made beside
		// the operator's notes.
		file := filepath.Join(c.Dir, "20261014130500-add-loop.box.org")
		if made.Plan != file || made.File != file || made.ID != "20261014130500-add-loop.box" ||
			made.Log != filepath.Join(c.Dir, "20261014130500-add-loop.box.log.org") {
			t.Errorf("made %+v", made)
		}
		data, _ := os.ReadFile(made.File)
		if header := "#+title: Add the loop\n#+TODO: TODO STARTED | DONE VERIFIED ABANDONED\n#+TODO: ASK | DECIDED(@) DROPPED(@) SUPERSEDED\n" +
			"#+FILETAGS: :r:box:\n"; !strings.Contains(string(data), ":END:\n"+header) {
			t.Errorf("the plan's file-level drawer is not followed by %q:\n%s", header, data)
		}
		if !strings.HasSuffix(string(data), ":END:\n** Goal\n** Steps\n** Distribution\n** Questions\n** Follow-ups\n** References\n** Related\n** Verification\n") {
			t.Errorf("the plan's sections:\n%s", data)
		}
		for _, s := range []string{"* Plan\n:PROPERTIES:\n",
			":CUSTOM_ID: 20261014130500-add-loop.box\n", ":HOST: box\n", ":REPOSITORIES: o/r\n", ":BRANCHES: main..loop\n",
			":WORKTREES: /w/loop\n", ":SESSIONS: claude:abc\n", ":HARNESS: claude\n", ":STATUS: planning\n",
			":PIPELINE: full\n"} {
			if !strings.Contains(string(data), s) {
				t.Errorf("the plan lacks %q:\n%s", s, data)
			}
		}
		if _, err := os.Stat(made.Log); err != nil {
			t.Errorf("no log: %v", err)
		}
		entries, _ := os.ReadDir(c.Dir)
		for _, e := range entries {
			if e.IsDir() {
				t.Errorf("New made a directory: %s", e.Name())
			}
		}
	})

	// Org's tags hold only letters, digits, _, @, # and %.
	t.Run("tags of repository and host", func(t *testing.T) {
		c := testConfig(t)
		c.Host = "ngrok-mac"
		made, err := c.New(NewOptions{Title: "T", Slug: "x", Repos: []string{"wesnel/nix-config", "wesnel/emacs.config.git",
			"other/nix-config"}}, day)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(made.File)
		if !strings.Contains(string(data), "\n#+FILETAGS: :nix_config:emacs_config:ngrok_mac:\n") {
			t.Errorf("plan:\n%s", data)
		}
	})

	// A plan's file is one org-roam node, at a file-level ID; its items'
	// IDs, the same in every plan, are in CUSTOM_ID, which org-id and
	// org-roam leave alone.
	t.Run("an org-roam node", func(t *testing.T) {
		c := testConfig(t)
		made, err := c.New(NewOptions{Title: "Add the loop", Slug: "add-loop"}, day)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(made.File)
		uuid := regexp.MustCompile(`\A:PROPERTIES:\n:ID: ([0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})\n:END:\n#\+title: Add the loop\n`)
		if !uuid.Match(data) {
			t.Errorf("the plan does not start with a file-level UUID ID before its title:\n%s", data)
		}
		if !strings.Contains(string(data), "* Plan\n:PROPERTIES:\n:CUSTOM_ID: "+made.ID+"\n") ||
			strings.Contains(string(data), "\n:ID: "+made.ID+"\n") {
			t.Errorf("the plan's ID is not its * Plan heading's CUSTOM_ID alone:\n%s", data)
		}
		other, err := c.New(NewOptions{Title: "Another", Slug: "another"}, day)
		if err != nil {
			t.Fatal(err)
		}
		again, _ := os.ReadFile(other.File)
		if m, n := uuid.FindSubmatch(data), regexp.MustCompile(`\A:PROPERTIES:\n:ID: (\S+)\n`).FindSubmatch(again); m != nil && n != nil &&
			string(m[1]) == string(n[1]) {
			t.Errorf("two plans share the file-level ID %s", m[1])
		}
	})

	t.Run("second plan with the slug", func(t *testing.T) {
		c := testConfig(t)
		newPlan(t, c, "add-loop")
		again, err := c.New(NewOptions{Title: "Again", Slug: "add-loop"}, day)
		if err != nil || again.File != filepath.Join(c.Dir, "20261014130500-add-loop-2.box.org") {
			t.Errorf("second plan with the slug: %+v, %v", again, err)
		}
	})

	for what, o := range map[string]NewOptions{
		"a slug with spaces":    {Title: "T", Slug: "Add loop"},
		"no title":              {Slug: "x"},
		"a title with newlines": {Title: "T\n* evil", Slug: "x"},
		"a worktree with space": {Title: "T", Slug: "x", Worktrees: []string{"/a b"}},
		"a part without parent": {Title: "T", Part: "P1"},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			c := testConfig(t)
			_, err := c.New(o, day)
			isRefusal(t, what, err)
		})
	}
}

func TestOpen(t *testing.T) {
	t.Parallel()
	// withPart makes a plan with a part, P1.
	withPart := func(t *testing.T) (Config, Plan) {
		t.Helper()
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Distribution", "P1")
		if _, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day); err != nil {
			t.Fatal(err)
		}
		return c, p
	}

	// Each case names the plan, and the part OpenFile says its file is.
	for name, c := range map[string]struct {
		arg  func(t *testing.T, p Plan) string
		part string
	}{
		"plan file": {func(_ *testing.T, p Plan) string { return p.file("") }, ""},
		"part file": {func(_ *testing.T, p Plan) string { return p.file("P1") }, "P1"},
		// A link is judged by the file it leads to, not its own name.
		"link to the part file": {func(t *testing.T, p Plan) string {
			link := filepath.Join(t.TempDir(), "goal.org")
			if err := os.Symlink(p.file("P1"), link); err != nil {
				t.Fatal(err)
			}
			return link
		}, "P1"},
		"log":  {func(_ *testing.T, p Plan) string { return p.log() }, ""},
		"name": {func(*testing.T, Plan) string { return "20261014130500-x.box" }, ""},
	} {
		t.Run("by "+name, func(t *testing.T) {
			cfg, p := withPart(t)
			arg := c.arg(t, p)
			if got, err := cfg.Open(arg); err != nil || got.Name != "20261014130500-x.box" || got.file("") != p.file("") {
				t.Errorf("Open(%s) = %+v, %v", arg, got, err)
			}
			if _, part, err := cfg.OpenFile(arg); err != nil || part != c.part {
				t.Errorf("OpenFile(%s) part %q, %v; want %q", arg, part, err, c.part)
			}
		})
	}

	// Each case names a plan to open, given the plans directory and a
	// directory outside it that holds a plan and its log.
	for name, arg := range map[string]func(dir, outside string) string{
		"plan outside the directory":    func(_, outside string) string { return filepath.Join(outside, "2026-10-14-x.box.org") },
		"the directory":                 func(dir, _ string) string { return dir },
		"another file in the directory": func(dir, _ string) string { return filepath.Join(dir, "notes.org") },
		"path out of the directory":     func(dir, _ string) string { return dir + "/../../elsewhere" },
		"unknown name":                  func(string, string) string { return "2026-10-14-y.box" },
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			c, _ := withPart(t)
			outside := t.TempDir()
			os.WriteFile(filepath.Join(outside, "2026-10-14-x.box.org"), []byte("* Plan\n"), 0o644)
			os.WriteFile(filepath.Join(outside, "2026-10-14-x.box.log.org"), nil, 0o644)
			os.WriteFile(filepath.Join(c.Dir, "notes.org"), nil, 0o644)
			arg := arg(c.Dir, outside)
			_, err := c.Open(arg)
			isRefusal(t, "Open "+arg, err)
		})
	}

	// agent-plan's writes run outside the sandbox, so PLAN, as Open and
	// new --parent take it, is never a file outside the plans directory,
	// however it is reached.
	for name, place := range map[string]func(t *testing.T, c Config, p Plan, outside string) string{
		"a link in the directory to a plan outside": func(t *testing.T, c Config, _ Plan, outside string) string {
			link := filepath.Join(c.Dir, "2026-10-14-z.box.org")
			os.Symlink(filepath.Join(outside, "2026-10-14-x.box.org"), link)
			os.WriteFile(filepath.Join(c.Dir, "2026-10-14-z.box.log.org"), nil, 0o644)
			return link
		},
		"a hard-linked plan file": func(t *testing.T, _ Config, p Plan, outside string) string {
			if err := os.Link(p.file(""), filepath.Join(outside, "copy.org")); err != nil {
				t.Fatal(err)
			}
			return p.file("")
		},
		"a plan beside the directory": func(t *testing.T, c Config, _ Plan, _ string) string {
			up := filepath.Dir(c.Dir)
			os.WriteFile(filepath.Join(up, "2026-10-14-x.mac.org"), []byte("* Plan\n"), 0o644)
			os.WriteFile(filepath.Join(up, "2026-10-14-x.mac.log.org"), nil, 0o644)
			return c.Dir + "/../2026-10-14-x.mac.org"
		},
		"a plan outside the directory": func(t *testing.T, _ Config, _ Plan, outside string) string {
			return filepath.Join(outside, "2026-10-14-x.box.org")
		},
	} {
		for _, as := range []string{"PLAN", "new --parent"} {
			t.Run("refused/"+as+"/"+name, func(t *testing.T) {
				c, p := withPart(t)
				outside := t.TempDir()
				os.WriteFile(filepath.Join(outside, "2026-10-14-x.box.org"), []byte("* Plan\n** Distribution\n:PROPERTIES:\n:ID: P2\n:END:\n"), 0o644)
				os.WriteFile(filepath.Join(outside, "2026-10-14-x.box.log.org"), nil, 0o644)
				arg := place(t, c, p, outside)
				var err error
				if as == "PLAN" {
					_, err = c.Open(arg)
				} else {
					_, err = c.New(NewOptions{Title: "Part", Parent: arg, Part: "P2"}, day)
				}
				isRefusal(t, as+" "+arg, err)
			})
		}
	}
}

// Plans are every host's plans, newest first, each its own file once: no
// part, log, other file, or link, even to a plan in the directory. A plan
// file that Open refuses, such as one without its log or with another
// name, is named among those that failed.
func TestPlans(t *testing.T) {
	c := testConfig(t)
	older := newPlan(t, c, "a")
	addItems(t, older.file(""), "** Distribution", "P1")
	if _, err := c.New(NewOptions{Title: "Part", Parent: older.file(""), Part: "P1"}, day); err != nil {
		t.Fatal(err)
	}
	theirs := newPlan(t, Config{Dir: c.Dir, Host: "other"}, "b")
	os.WriteFile(filepath.Join(c.Dir, "notes.org"), nil, 0o644)
	outside := t.TempDir()
	for _, name := range []string{"2026-10-14-z.box.org", "2026-10-14-z.box.log.org"} {
		os.WriteFile(filepath.Join(outside, name), []byte("* Plan\n"), 0o644)
		os.Symlink(filepath.Join(outside, name), filepath.Join(c.Dir, name))
	}
	os.Symlink(older.file(""), filepath.Join(c.Dir, "2026-10-14-c.box.org"))
	os.Symlink(older.log(), filepath.Join(c.Dir, "2026-10-14-c.box.log.org"))
	for _, name := range []string{"2026-10-14-h.box.org", "2026-10-14-h.box.log.org"} {
		os.WriteFile(filepath.Join(outside, name), []byte("* Plan\n"), 0o644)
		if err := os.Link(filepath.Join(outside, name), filepath.Join(c.Dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(c.Dir, "2026-10-14-m.box.org"), []byte("* Plan\n"), 0o644)
	got, failed, err := c.Plans()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(failed, ", ") != "2026-10-14-m.box, 2026-10-14-h.box" {
		t.Errorf("failed %q", failed)
	}
	var names []string
	for _, p := range got {
		names = append(names, fmt.Sprintf("%s own=%v", p.Name, p.Own))
	}
	want := theirs.Name + " own=false, " + older.Name + " own=true"
	if strings.Join(names, ", ") != want {
		t.Errorf("Plans = %s, want %s", strings.Join(names, ", "), want)
	}
}

// Plans named either way are newest first by their dates, a YYYY-MM-DD
// name read as YYYYMMDD and so before any plan named the org-roam way
// that day, as the plan views in emacs-config order them.
func TestPlansOrder(t *testing.T) {
	c := testConfig(t)
	os.MkdirAll(c.Dir, 0o755)
	names := []string{"2025-12-31-a.box", "20260101000000-b.box", "2026-10-17-c.box", "20261017000001-d.box",
		"2026-10-23-e.box", "20261023143000-f.box"}
	for _, name := range names {
		os.WriteFile(filepath.Join(c.Dir, name+".org"), []byte("* Plan\n"), 0o644)
		os.WriteFile(filepath.Join(c.Dir, name+".log.org"), nil, 0o644)
	}
	got, failed, err := c.Plans()
	if err != nil || len(failed) != 0 {
		t.Fatal(failed, err)
	}
	var order []string
	for _, p := range got {
		order = append(order, p.Name)
	}
	if want := "20261023143000-f.box 2026-10-23-e.box 20261017000001-d.box 2026-10-17-c.box 20260101000000-b.box 2025-12-31-a.box"; strings.Join(order, " ") != want {
		t.Errorf("Plans = %s, want %s", strings.Join(order, " "), want)
	}
}

// readPlan reads a plan's file only as the one regular file it opened: a
// link, a file with another name or a FIFO put there after Open checked
// it is refused, and a FIFO without blocking.
func TestReadPlan(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.org")
	os.WriteFile(plain, []byte("* Plan\n"), 0o644)
	if data, err := readPlan(plain); err != nil || string(data) != "* Plan\n" {
		t.Errorf("a plain file: %q, %v", data, err)
	}
	os.Symlink(plain, filepath.Join(dir, "link.org"))
	other := filepath.Join(dir, "other.org")
	os.WriteFile(other, []byte("* Secret\n"), 0o644)
	os.Link(other, filepath.Join(dir, "hard.org"))
	if err := exec.Command("mkfifo", filepath.Join(dir, "fifo.org")).Run(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link.org", "hard.org", "fifo.org"} {
		done := make(chan error, 1)
		go func() {
			_, err := readPlan(filepath.Join(dir, name))
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s was read", name)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("reading %s blocked", name)
		}
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, gitenv.Without(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestInit(t *testing.T) {
	t.Run("in a repository", testInitInRepository)
	t.Run("clone without the key", testInitCloneWithoutKey)
	t.Run("clone with the key", testInitCloneWithKey)
	t.Run("retry a rejected clone", testInitCloneRetry)
	t.Run("recover generated leftovers", testInitCloneLeftovers)
	t.Run("preserve other contents", testInitClonePreserves)
	t.Run("concurrent clone", testInitConcurrent)
	t.Run("converges a clone", testInitConverges)
}

// Init readies the plans directory for a session whose sandbox may write
// only there, keeping the logs' lock files out of the repository around
// it, and changes nothing else.
func testInitInRepository(t *testing.T) {
	t.Parallel()
	c := testConfig(t)
	outer := filepath.Dir(filepath.Dir(c.Dir))
	gitIn(t, outer, "init", "-q", "-b", "main")
	before, _ := os.ReadFile(filepath.Join(outer, ".git", "info", "exclude"))
	for i := 0; i < 2; i++ {
		got, err := c.Init()
		if err != nil || got.Dir != c.Dir {
			t.Fatalf("Init: %+v, %v", got, err)
		}
	}
	if ignore, _ := os.ReadFile(filepath.Join(c.Dir, ".gitignore")); string(ignore) != "*.lock\n" {
		t.Errorf(".gitignore: %q", ignore)
	}
	if after, _ := os.ReadFile(filepath.Join(outer, ".git", "info", "exclude")); string(before) != string(after) {
		t.Errorf("the repository's exclude changed:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(c.Dir), ".git")); err == nil {
		t.Error("Init made a repository")
	}
	newPlan(t, c, "x")
	status := gitIn(t, outer, "status", "--porcelain", "--untracked-files=all")
	if strings.Contains(status, ".lock") {
		t.Errorf("a lock file shows in the repository:\n%s", status)
	}
}

func TestPart(t *testing.T) {
	// distributing makes a plan that distributes P1.
	distributing := func(t *testing.T) (Config, Plan) {
		t.Helper()
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Distribution", "P1")
		return c, p
	}

	t.Run("made", func(t *testing.T) {
		c, p := distributing(t)
		made, err := c.New(NewOptions{Title: "The loop's part", Parent: p.file(""), Part: "P1",
			Branches: []string{"loop..part"}}, day)
		if err != nil {
			t.Fatal(err)
		}
		if made.File != filepath.Join(c.Dir, "20261014130500-x.box.P1.org") || made.Plan != p.file("") || made.ID != "P1" ||
			made.Log != p.log() {
			t.Errorf("made %+v", made)
		}
		data, _ := os.ReadFile(made.File)
		for _, s := range []string{"#+title: The loop's part\n", ":CUSTOM_ID: P1\n", ":PARENT: [[file:20261014130500-x.box.org]]\n",
			":BRANCHES: loop..part\n", "** Goal\n", "** Steps\n"} {
			if !strings.Contains(string(data), s) {
				t.Errorf("the part lacks %q:\n%s", s, data)
			}
		}
	})

	t.Run("refused/not distributed", func(t *testing.T) {
		c, p := distributing(t)
		_, err := c.New(NewOptions{Title: "T", Parent: p.file(""), Part: "P2"}, day)
		isRefusal(t, "a part the parent does not distribute", err)
	})

	t.Run("refused/made twice", func(t *testing.T) {
		c, p := distributing(t)
		if _, err := c.New(NewOptions{Title: "The loop's part", Parent: p.file(""), Part: "P1"}, day); err != nil {
			t.Fatal(err)
		}
		_, err := c.New(NewOptions{Title: "T", Parent: p.file(""), Part: "P1"}, day)
		isRefusal(t, "a part made twice", err)
	})

	t.Run("refused/no ID", func(t *testing.T) {
		c, p := distributing(t)
		_, err := c.New(NewOptions{Title: "T", Parent: p.file(""), Part: "../x"}, day)
		isRefusal(t, "a part that is no ID", err)
	})
}

// Another host's plan sits beside this host's and can be read, but only its
// own host writes it.
func TestOtherHost(t *testing.T) {
	// theirs makes a plan of another host, with a step S1 and a part P1, and
	// opens it as this host.
	theirs := func(t *testing.T) (Config, Plan) {
		t.Helper()
		c := testConfig(t)
		theirs := newPlan(t, Config{Dir: c.Dir, Host: "other"}, "x")
		addItems(t, theirs.file(""), "** Steps", "S1")
		addItems(t, theirs.file(""), "** Distribution", "P1")
		p, err := c.Open(theirs.file(""))
		if err != nil || p.Name != "20261014130500-x.other" {
			t.Fatalf("Open: %+v, %v", p, err)
		}
		return c, p
	}

	t.Run("read", func(t *testing.T) {
		_, p := theirs(t)
		if _, err := p.Status(); err != nil {
			t.Errorf("status: %v", err)
		}
	})

	t.Run("refused/log", func(t *testing.T) {
		_, p := theirs(t)
		before, _ := os.ReadFile(p.log())
		_, err := p.Log(Event{Kind: "started", Item: "S1"}, day)
		isRefusal(t, "logging to another host's plan", err)
		if after, _ := os.ReadFile(p.log()); string(after) != string(before) {
			t.Errorf("another host's log changed:\n%s", after)
		}
	})

	t.Run("refused/part", func(t *testing.T) {
		c, p := theirs(t)
		before, _ := os.ReadFile(p.log())
		_, err := c.New(NewOptions{Title: "T", Parent: p.file(""), Part: "P1"}, day)
		isRefusal(t, "a part of another host's plan", err)
		if after, _ := os.ReadFile(p.log()); string(after) != string(before) {
			t.Errorf("another host's log changed:\n%s", after)
		}
	})
}

func TestLog(t *testing.T) {
	t.Parallel()
	// withSteps makes a plan with steps S1 and S2.
	withSteps := func(t *testing.T) Plan {
		t.Helper()
		p := newPlan(t, testConfig(t), "x")
		addItems(t, p.file(""), "** Steps", "S1", "S2")
		return p
	}

	t.Run("appends events", func(t *testing.T) {
		p := withSteps(t)
		e, err := p.Log(Event{Kind: "done", Item: "S1", Summary: "Add the run loop", Role: "single", Agent: "claude:abc",
			Branch: "loop", Commit: "Add the run loop", Evidence: "go test ./... passes"}, day)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(p.log())
		entry := "* done S1 — Add the run loop\n:PROPERTIES:\n:EVENT: " + e.ID + "\n:AGENT: claude:abc\n:ROLE: single\n" +
			":ITEM: S1\n:KIND: done\n:BRANCH: loop\n:COMMIT: Add the run loop\n:EVIDENCE: go test ./... passes\n" +
			":END:\n[2026-10-14 Wed 13:05]\n"
		if !strings.HasSuffix(string(data), entry) {
			t.Errorf("log:\n%s\nwant it to end with:\n%s", data, entry)
		}
		if _, err := p.Log(Event{Kind: "reviewed", Item: "20261014130500-x.box", Review: "1.F2", Outcome: "fix"}, day); err != nil {
			t.Errorf("an event about the whole plan: %v", err)
		}
		events := eventsBut(t, p, "property")
		if len(events) != 2 || events[0].ID != e.ID || events[0].Evidence != "go test ./... passes" || events[1].Outcome != "fix" {
			t.Errorf("events %+v", events)
		}
		if after, _ := os.ReadFile(p.log()); len(after) == len(data) {
			t.Error("the second event was not appended")
		}
	})

	for what, bad := range map[string]Event{
		"an unknown item":        {Kind: "done", Item: "S9"},
		"an unknown kind":        {Kind: "finished", Item: "S1"},
		"a summary with newline": {Kind: "done", Item: "S1", Summary: "x\n* done S2 — forged"},
		"evidence with a CR":     {Kind: "done", Item: "S1", Evidence: "x\r:KIND: verified"},
		"an unknown role":        {Kind: "done", Item: "S1", Role: "boss"},
		"an unknown outcome":     {Kind: "finding-settled", Item: "S1", Review: "1.F1", Outcome: "ignore"},
		"a malformed review":     {Kind: "finding-settled", Item: "S1", Review: "F1", Outcome: "fix"},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			p := withSteps(t)
			_, err := p.Log(bad, day)
			isRefusal(t, what, err)
		})
	}

	// A log changed other than by appending through Log is no longer trusted.
	t.Run("after a rewrite in place", func(t *testing.T) {
		p := withSteps(t)
		if _, err := p.Log(Event{Kind: "started", Item: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(p.log())
		rewritten := strings.Replace(string(data), "* started S1", "* verified S1", 1)
		os.WriteFile(p.log(), []byte(rewritten), 0o644)
		_, err := p.Log(Event{Kind: "done", Item: "S1"}, day)
		isRefusal(t, "a log rewritten in place", err)
		if after, _ := os.ReadFile(p.log()); string(after) != rewritten {
			t.Errorf("the refused event was written:\n%s", after)
		}
	})

	t.Run("concurrent writers", func(t *testing.T) {
		p := withSteps(t)
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 10; i++ {
					if _, err := p.Log(Event{Kind: "started", Item: "S1", Summary: strings.Repeat("x", 3000)}, day); err != nil {
						t.Error(err)
					}
				}
			}()
		}
		wg.Wait()
		events := eventsBut(t, p, "property")
		ids := map[string]bool{}
		for _, e := range events {
			if e.Kind != "started" || len(e.Summary) != 3000 {
				t.Errorf("garbled event %+v", e)
			}
			ids[e.ID] = true
		}
		if len(events) != 80 || len(ids) != 80 {
			t.Errorf("%d events, %d distinct", len(events), len(ids))
		}
		if _, err := p.Log(Event{Kind: "done", Item: "S1"}, day); err != nil {
			t.Errorf("after concurrent writers: %v", err)
		}
	})
}

func TestStatus(t *testing.T) {
	t.Parallel()
	t.Run("items", testStatusItems)
	t.Run("ready parts", testStatusReadyParts)
	t.Run("ready steps", testStatusReadySteps)
}

func testStatusItems(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	plan := p.file("")
	addItems(t, plan, "** Steps", "S1", "S2", "S3")
	addItems(t, plan, "** Distribution", "P1")
	part, err := c.New(NewOptions{Title: "Part", Parent: plan, Part: "P1"}, day)
	if err != nil {
		t.Fatal(err)
	}
	addItems(t, part.File, "** Steps", "P1.S1")
	for _, e := range []Event{
		{Kind: "done", Item: "S1", Commit: "Add the run loop"},
		{Kind: "verified", Item: "S1"},
		{Kind: "done", Item: "S2", Commit: "Add the flag"},
		{Kind: "done", Item: "P1.S1", Commit: "Add the part"},
		{Kind: "reviewed", Item: "P1"},
		{Kind: "started", Item: "S3"},
	} {
		if _, err := p.Log(e, day); err != nil {
			t.Fatal(err)
		}
	}
	s, err := p.Status()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.NotDone, " ") != "S3" || strings.Join(s.DoneNotVerified, " ") != "S2 P1.S1" ||
		s.Implemented != "1/4" || s.Verified != 1 || s.Total != 4 || s.State != "planning" || s.Title != "Add the loop" ||
		s.Plan != plan {
		t.Errorf("status %+v", s)
	}
	var keywords []string
	for _, step := range s.Steps {
		keywords = append(keywords, step.ID+" "+step.Keyword)
	}
	if got := strings.Join(keywords, ", "); got != "S1 VERIFIED, S2 DONE, S3 STARTED, P1.S1 DONE" {
		t.Errorf("keywords %s", got)
	}
	org := s.Org()
	for _, line := range []string{"| S1 | Step S1 | yes | yes | Add the run loop |", "| P1.S1 | Step P1.S1 | yes | no | Add the part |",
		"| S3 | Step S3 | no | no | |", "- Implemented: 1/4 verified", "- Without done: S3", "- Done, not verified: S2, P1.S1"} {
		if !strings.Contains(org, line) {
			t.Errorf("org status lacks %q:\n%s", line, org)
		}
	}
}

func TestGoal(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		_, err := p.Goal("")
		isRefusal(t, "an empty goal", err)
	})

	t.Run("set", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		setGoal(t, p.file(""), "Add the loop.\n\nNot this change's job: the flag.")
		if goal, err := p.Goal(""); err != nil || goal != "Add the loop.\n\nNot this change's job: the flag.\n" {
			t.Errorf("goal %q, %v", goal, err)
		}
	})

	t.Run("part", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Distribution", "P1")
		part, _ := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day)
		setGoal(t, part.File, "The part.")
		if goal, err := p.Goal("P1"); err != nil || goal != "The part.\n" {
			t.Errorf("part goal %q, %v", goal, err)
		}
	})

	t.Run("part with no file", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		_, err := p.Goal("P7")
		isRefusal(t, "a part with no file", err)
	})
}

func TestFind(t *testing.T) {
	t.Parallel()
	t.Run("ownership only", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		worktree := t.TempDir()
		if _, err := p.SetOwner(worktree, "builder", day); err != nil {
			t.Fatal(err)
		}
		found, err := c.Find(FindOptions{Worktree: worktree})
		if err != nil || len(found) != 1 || found[0].Plan != p.file("") || strings.Join(found[0].Matched, " ") != "worktree" {
			t.Errorf("ownership-only worktree: %+v, %v", found, err)
		}
		if found, err := c.Find(FindOptions{Worktree: t.TempDir()}); err != nil || len(found) != 0 {
			t.Errorf("unrelated worktree: %+v, %v", found, err)
		}
	})

	// twoHosts makes a plan on this host and one of the same name on
	// another, and returns this host's.
	twoHosts := func(t *testing.T) (Config, Plan) {
		t.Helper()
		c := testConfig(t)
		p := newPlan(t, c, "x")
		newPlan(t, Config{Dir: c.Dir, Host: "other"}, "x")
		return c, p
	}

	for what, o := range map[string]FindOptions{
		"session": {Session: "abc"}, "harness session": {Session: "claude:abc"},
		"worktree": {Worktree: "/w/loop"}, "branch": {Branch: "loop"},
	} {
		t.Run("by "+what, func(t *testing.T) {
			c, p := twoHosts(t)
			found, err := c.Find(o)
			if err != nil || len(found) != 1 || found[0].Plan != p.file("") || found[0].File != p.file("") {
				t.Errorf("by %s: %+v, %v", what, found, err)
			}
		})
	}

	for what, o := range map[string]FindOptions{
		"part of a session":  {Session: "ab"},
		"parent of worktree": {Worktree: "/w"},
		"another branch":     {Branch: "main"},
	} {
		t.Run("none/"+what, func(t *testing.T) {
			c, _ := twoHosts(t)
			if found, err := c.Find(o); err != nil || len(found) != 0 {
				t.Errorf("%+v found %+v, %v", o, found, err)
			}
		})
	}
}

func hookContext(t *testing.T, out []byte, event string) string {
	t.Helper()
	var v struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &v); err != nil || v.HookSpecificOutput.HookEventName != event {
		t.Fatalf("hook output %s: %v", out, err)
	}
	return v.HookSpecificOutput.AdditionalContext
}

func TestSessionHook(t *testing.T) {
	t.Run("the session's plan", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		out, err := c.SessionHook([]byte(`{"session_id":"abc","source":"compact","cwd":"/nowhere"}`))
		if err != nil {
			t.Fatal(err)
		}
		if context := hookContext(t, out, "SessionStart"); !strings.Contains(context, p.file("")) {
			t.Errorf("context %q", context)
		}
	})

	t.Run("no plan", func(t *testing.T) {
		c := testConfig(t)
		newPlan(t, c, "x")
		if out, err := c.SessionHook([]byte(`{"session_id":"zzz","source":"resume","cwd":"/nowhere"}`)); err != nil || len(out) != 0 {
			t.Errorf("no plan: %s, %v", out, err)
		}
	})
}

func TestLoad(t *testing.T) {
	// useConfig points AGENT_TOOLS_CONFIG at a file of the test's own, with
	// the content given, or none.
	useConfig := func(t *testing.T, content string) {
		t.Helper()
		config := filepath.Join(t.TempDir(), "config.json")
		t.Setenv("AGENT_TOOLS_CONFIG", config)
		if content != "" {
			os.WriteFile(config, []byte(content), 0o644)
		}
	}

	// Unconfigured, plans live in the operator's notes repository, whose
	// root, not the directory above the plans, is the repository sync
	// commits to.
	t.Run("without a config", func(t *testing.T) {
		useConfig(t, "")
		home, _ := os.UserHomeDir()
		c, err := Load()
		if err != nil || c.Dir != filepath.Join(home, "git", "github.com", "wesnel", "notes", "agents", "plans") || c.Host == "" {
			t.Errorf("without a config: %+v, %v", c, err)
		}
		if repo := c.repo(); repo != filepath.Join(home, "git", "github.com", "wesnel", "notes") {
			t.Errorf("without a config, the plans repository is %s", repo)
		}
	})

	// A plans directory the config names is in the repository above it
	// unless plans.repo says otherwise.
	t.Run("from the config", func(t *testing.T) {
		useConfig(t, `{"repos": {}, "plans": {"dir": "/p/plans", "host": "ngrok"}}`)
		if c, err := Load(); err != nil || c.Dir != "/p/plans" || c.Host != "ngrok" || c.repo() != "/p" {
			t.Errorf("from the config: %+v, %v", c, err)
		}
	})

	t.Run("a config without a directory", func(t *testing.T) {
		useConfig(t, `{"plans": {"host": "ngrok"}}`)
		home, _ := os.UserHomeDir()
		notes := filepath.Join(home, "git", "github.com", "wesnel", "notes")
		if c, err := Load(); err != nil || c.Dir != filepath.Join(notes, "agents", "plans") || c.repo() != notes || c.Host != "ngrok" {
			t.Errorf("a config without a directory: %+v, %v", c, err)
		}
	})

	t.Run("templates from the config", func(t *testing.T) {
		useConfig(t, `{"plans": {"dir": "/p", "host": "ngrok", "templates": "/t"}}`)
		if c, err := Load(); err != nil || c.Templates != "/t" {
			t.Errorf("templates from the config: %+v, %v", c, err)
		}
	})

	t.Run("relative templates", func(t *testing.T) {
		useConfig(t, `{"plans": {"dir": "/p", "host": "ngrok", "templates": "t"}}`)
		if c, err := Load(); err == nil {
			t.Errorf("relative templates: %+v", c)
		}
	})

	// A dot would make a plan's file names ambiguous.
	t.Run("host with a dot", func(t *testing.T) {
		useConfig(t, `{"plans": {"dir": "/p", "host": "a.b"}}`)
		if c, err := Load(); err == nil {
			t.Errorf("a host with a dot: %+v", c)
		}
	})
}
