package plans

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOwners(t *testing.T) {
	t.Run("none before any owner", func(t *testing.T) {
		c := testConfig(t)
		newPlan(t, c, "x")
		if owners, err := c.Owners(); err != nil || len(owners) != 0 {
			t.Fatalf("before any owner: %v %v", owners, err)
		}
	})

	t.Run("set", func(t *testing.T) {
		c := testConfig(t)
		wt, _ := filepath.EvalSymlinks(t.TempDir())
		other, _ := filepath.EvalSymlinks(t.TempDir())
		p := newPlan(t, c, "x")
		if _, err := p.SetOwner(wt, "a1", day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.SetOwner(other, "main", day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.SetOwner(wt, "a2", day); err != nil {
			t.Fatal(err)
		}
		owners, err := c.Owners()
		if err != nil || owners[wt] != "a2" || owners[other] != "main" || len(owners) != 2 {
			t.Errorf("owners: %v %v", owners, err)
		}
		data, _ := os.ReadFile(p.File())
		if strings.Count(string(data), ":OWNERS:") != 1 || !strings.Contains(string(data), ":OWNERS: "+wt+"=a2 "+other+"=main\n") {
			t.Errorf("plan file:\n%s", data)
		}
		if found, _ := c.Find(FindOptions{Session: "abc"}); len(found) != 1 {
			t.Errorf("the plan no longer parses: %v", found)
		}
	})

	// Each case is a worktree and an owner; WT stands for a worktree of the
	// test's own.
	for name, bad := range map[string][2]string{
		"relative worktree":    {"relative/wt", "a1"},
		"owner of two words":   {"WT", "two words"},
		"owner with an equals": {"WT", "a=b"},
		"no owner":             {"WT", ""},
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			wt, _ := filepath.EvalSymlinks(t.TempDir())
			p := newPlan(t, testConfig(t), "x")
			dir := strings.Replace(bad[0], "WT", wt, 1)
			_, err := p.SetOwner(dir, bad[1], day)
			isRefusal(t, "Own "+dir+" "+bad[1], err)
		})
	}

	t.Run("refused/another host's plan", func(t *testing.T) {
		wt, _ := filepath.EvalSymlinks(t.TempDir())
		p := newPlan(t, testConfig(t), "x")
		_, err := otherHosts(t, p).SetOwner(wt, "a3", day)
		isRefusal(t, "another host's plan", err)
	})
}

func TestSessionWorktrees(t *testing.T) {
	// trees makes n directories of the test's own, by their real paths.
	trees := func(t *testing.T, n int) []string {
		t.Helper()
		var out []string
		for range n {
			dir, _ := filepath.EvalSymlinks(t.TempDir())
			out = append(out, dir)
		}
		return out
	}
	// plan writes a plan, or with parent its part P1, made hours after day,
	// naming session in SESSIONS and worktrees in WORKTREES.
	plan := func(t *testing.T, c Config, slug, parent string, hours int, session string, worktrees ...string) Created {
		t.Helper()
		o := NewOptions{Title: "Work", Slug: slug, Sessions: []string{session}, Worktrees: worktrees, Harness: "claude"}
		if parent != "" {
			addItems(t, parent, "** Distribution", "P1")
			o.Title, o.Slug, o.Parent, o.Part = "Part", "", parent, "P1"
		}
		made, err := c.New(o, day.Add(time.Duration(hours)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return made
	}
	own := func(t *testing.T, c Config, file, worktree, agent string) {
		t.Helper()
		p, err := c.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.SetOwner(worktree, agent, day); err != nil {
			t.Fatal(err)
		}
	}
	want := func(t *testing.T, c Config, session string, expected ...string) {
		t.Helper()
		got, err := c.SessionWorktrees(session)
		if err != nil || strings.Join(got, " ") != strings.Join(expected, " ") {
			t.Errorf("SessionWorktrees(%q) = %q, %v; want %q", session, got, err, expected)
		}
	}

	// A plan names the session and a worktree; another plan's part, not
	// its own file, names the session and another worktree.
	t.Run("plans and parts naming the session", func(t *testing.T) {
		c := testConfig(t)
		w := trees(t, 3)
		plan(t, c, "mine", "", 0, "claude:abc", w[0])
		parent := plan(t, c, "theirs", "", 1, "claude:other", w[2])
		plan(t, c, "", parent.Plan, 0, "claude:abc", w[1])
		want(t, c, "claude:abc", w[1], w[0])
		want(t, c, "claude:other", w[2])
	})

	// The session's plan names every worktree; any plan of this host may
	// give one its owner, the newest plan's owner winning.
	t.Run("owners", func(t *testing.T) {
		c := testConfig(t)
		w := trees(t, 5)
		agent, mainOwned, masked, unmasked, free := w[0], w[1], w[2], w[3], w[4]
		mine := plan(t, c, "mine", "", 0, "claude:abc", w...)
		own(t, c, mine.Plan, agent, "a1")
		own(t, c, mine.Plan, mainOwned, MainAgent)
		own(t, c, mine.Plan, masked, MainAgent)
		own(t, c, mine.Plan, unmasked, "a1")
		newer := plan(t, c, "newer", "", 1, "claude:abc")
		own(t, c, newer.Plan, masked, "a2")
		own(t, c, newer.Plan, unmasked, MainAgent)
		want(t, c, "claude:abc", mainOwned, unmasked, free)
	})

	// The newest plan naming a worktree, in WORKTREES or OWNERS, decides
	// whose it is, as the guard decides which session may edit there.
	t.Run("a newer plan of another session", func(t *testing.T) {
		c := testConfig(t)
		w := trees(t, 2)
		taken, free := w[0], w[1]
		plan(t, c, "mine", "", 0, "claude:abc", taken, free)
		plan(t, c, "theirs", "", 1, "claude:other", taken)
		want(t, c, "claude:abc", free)
	})

	// spell writes OWNERS of a plan's file as given, unlike SetOwner, which
	// resolves the worktree first.
	spell := func(t *testing.T, c Config, file, worktree, agent string) {
		t.Helper()
		own(t, c, file, worktree, agent)
		setProp(t, file, "OWNERS", worktree+"="+agent)
	}

	// An owner names a worktree by any spelling of it.
	t.Run("owner spelled in another case", func(t *testing.T) {
		c := testConfig(t)
		w := trees(t, 2)
		owned := filepath.Join(w[0], "wt")
		if err := os.Mkdir(owned, 0o755); err != nil {
			t.Fatal(err)
		}
		a, errA := os.Stat(owned)
		b, errB := os.Stat(filepath.Join(w[0], "WT"))
		if errA != nil || errB != nil || !os.SameFile(a, b) {
			t.Skip("the temporary directory's file system is case-sensitive, so no other case names the worktree")
		}
		mine := plan(t, c, "mine", "", 0, "claude:abc", owned, w[1])
		spell(t, c, mine.Plan, filepath.Join(w[0], "WT"), "a1")
		want(t, c, "claude:abc", w[1])
	})

	// Two plans spell one worktree's owner differently, through a symbolic
	// link; the newer plan's owner wins either way, every time.
	for name, owners := range map[string][2]string{
		"newer owner another agent": {MainAgent, "a1"},
		"newer owner main":          {"a1", MainAgent},
	} {
		t.Run("owner spelled through a link/"+name, func(t *testing.T) {
			c := testConfig(t)
			real := trees(t, 1)[0]
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}
			older := plan(t, c, "older", "", 0, "claude:abc", real)
			spell(t, c, older.Plan, real, owners[0])
			newer := plan(t, c, "newer", "", 1, "claude:abc")
			spell(t, c, newer.Plan, link, owners[1])
			var expected []string
			if owners[1] == MainAgent {
				expected = []string{real}
			}
			for range 20 {
				want(t, c, "claude:abc", expected...)
			}
		})
	}

	t.Run("another host's plans", func(t *testing.T) {
		c := testConfig(t)
		w := trees(t, 2)
		plan(t, c, "mine", "", 0, "claude:abc", w[0])
		plan(t, Config{Dir: c.Dir, Host: "other"}, "theirs", "", 1, "claude:abc", w[1])
		want(t, c, "claude:abc", w[0])
	})

	t.Run("refused/no session", func(t *testing.T) {
		_, err := testConfig(t).SessionWorktrees("")
		isRefusal(t, "no session", err)
	})
}
