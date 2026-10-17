package dashboard

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/queue"
)

// script runs w, one poll every 30 seconds of a fake clock from start, with
// steps[i] run before poll i+1, and returns what each poll printed.
type script struct {
	w     Watch
	clock time.Time
	pulls int
	sync  plans.SyncState
}

func newScript(t *testing.T, c plans.Config, p queue.Paths) *script {
	t.Helper()
	s := &script{clock: now}
	var out bytes.Buffer
	s.w = Watch{Paths: p, Config: c, Out: &out,
		Now:  func() time.Time { return s.clock },
		Sync: func() (plans.SyncState, error) { return s.sync, nil },
		Pull: func() { s.pulls++ }}
	return s
}

func (s *script) run(t *testing.T, steps ...func()) []string {
	t.Helper()
	out := s.w.Out.(*bytes.Buffer)
	out.Reset()
	var printed []string
	mark := 0
	i := 0
	s.w.Wait = func(ctx context.Context, d time.Duration) bool {
		if d != 30*time.Second {
			t.Errorf("waited %v", d)
		}
		printed = append(printed, out.String()[mark:])
		mark = out.Len()
		if i == len(steps) {
			return false
		}
		steps[i]()
		i++
		s.clock = s.clock.Add(d)
		return true
	}
	if err := s.w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if i != len(steps) {
		t.Fatalf("ran %d of %d steps; printed %q", i, len(steps), out.String())
	}
	return printed
}

func nothing() {}

// watchFixture is box's plan in a plans directory, with a step, and the
// state directory beside it.
func watchFixture(t *testing.T) (plans.Config, plans.Plan, queue.Paths) {
	t.Helper()
	root := t.TempDir()
	c := plans.Config{Dir: filepath.Join(root, "notes", "agents"), Host: "box"}
	made, err := c.New(plans.NewOptions{Title: "Loop", Slug: "loop", Goal: "Run the loop."}, day)
	if err != nil {
		t.Fatal(err)
	}
	own, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	under(t, own.File(), "** Steps", "*** TODO Write the loop\n:PROPERTIES:\n:ID: S1\n:END:\n")
	if _, err := own.Ask(plans.AskOptions{Question: "Keep the flag?", Recommendation: "no"}, day); err != nil {
		t.Fatal(err)
	}
	return c, own, queue.Paths{Dir: filepath.Join(root, "state", "agent-plans")}
}

func ask(t *testing.T, p plans.Plan, q string) func() {
	return func() {
		if _, err := p.Ask(plans.AskOptions{Question: q, Recommendation: "yes"}, day); err != nil {
			t.Fatal(err)
		}
	}
}

// A line is printed when what the page would show of the plans changes, not
// when only a log does, at most once every 90 seconds, and never for the
// state the first watch found.
func TestWatchChanges(t *testing.T) {
	c, own, p := watchFixture(t)
	s := newScript(t, c, p)
	logged := func() {
		if _, err := own.Log(plans.Event{Kind: "discovered", Item: "Q1", Summary: "a log line"}, day.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	got := s.run(t, logged, ask(t, own, "Rename it?"), ask(t, own, "Split it?"), nothing, nothing, nothing)
	want := []string{"", "", LineChanged + "\n", "", "", LineChanged + "\n", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("printed %q, want %q", got, want)
	}

	// A change while no watch ran is printed by the next at once; nothing
	// unchanged is printed again.
	ask(t, own, "Ship it?")()
	s.clock = s.clock.Add(time.Hour)
	if got := s.run(t, nothing); !reflect.DeepEqual(got, []string{LineChanged + "\n", ""}) {
		t.Errorf("after a restart: %q", got)
	}
	if got := s.run(t, nothing); !reflect.DeepEqual(got, []string{"", ""}) {
		t.Errorf("unchanged: %q", got)
	}
}

// Pushes and fetches alone change nothing printed, but whether this host's
// plans are unsynced does; Pull runs at the start and every ten minutes.
func TestWatchSyncAndPull(t *testing.T) {
	c, _, p := watchFixture(t)
	s := newScript(t, c, p)
	s.sync = plans.SyncState{Repo: true, Pushed: now.Add(-time.Hour), Fetched: now}
	steps := make([]func(), 41)
	for i := range steps {
		steps[i] = nothing
	}
	steps[0] = func() { s.sync.Fetched = s.clock }
	steps[1] = func() { s.sync.Pushed = s.clock }
	steps[3] = func() { s.sync.Unsynced = true }
	got := s.run(t, steps...)
	for i, line := range got {
		if want := map[int]string{4: LineChanged + "\n"}[i]; line != want {
			t.Errorf("poll %d printed %q, want %q", i, line, want)
		}
	}
	if s.pulls != 3 {
		t.Errorf("%d pulls over 20 minutes", s.pulls)
	}
}

// A stall, this host's plans unsynced and its last push over six hours
// old, is printed once, a restart included, and again once it has ended
// and come back.
func TestWatchStall(t *testing.T) {
	c, _, p := watchFixture(t)
	s := newScript(t, c, p)
	s.sync = plans.SyncState{Repo: true, Unsynced: true, Pushed: now.Add(-5 * time.Hour)}
	stall := func() { s.sync.Pushed = s.clock.Add(-6*time.Hour - time.Minute) }
	got := s.run(t, nothing, stall, nothing, nothing)
	if strings.Join(got, "") != LineStalled+"\n" || got[2] != LineStalled+"\n" {
		t.Errorf("printed %q", got)
	}
	if got := s.run(t, nothing); strings.Join(got, "") != "" {
		t.Errorf("after a restart: %q", got)
	}
	ended := func() { s.sync.Unsynced, s.sync.Pushed = false, s.clock }
	again := func() { s.sync.Unsynced, s.sync.Pushed = true, s.clock.Add(-7*time.Hour) }
	got = s.run(t, ended, nothing, again, nothing)
	if strings.Count(strings.Join(got, ""), LineStalled) != 1 || got[3] != LineStalled+"\n" {
		t.Errorf("printed %q", got)
	}
	// Never pushed, or no repository, is no stall.
	s.sync = plans.SyncState{Unsynced: true, Pushed: now.Add(-24 * time.Hour)}
	s.run(t, nothing)
	s.sync = plans.SyncState{Repo: true, Unsynced: true}
	if got := s.run(t, nothing, nothing); strings.Contains(strings.Join(got, ""), LineStalled) {
		t.Errorf("printed %q", got)
	}
}

// A digest committed to the notes repository after the first watch is
// announced once, by kind; other files there are not.
func TestWatchDigest(t *testing.T) {
	c, _, p := watchFixture(t)
	digests := filepath.Join(filepath.Dir(c.Dir), "digests", "2026")
	put := func(name string) func() {
		return func() {
			os.MkdirAll(digests, 0o755)
			if err := os.WriteFile(filepath.Join(digests, name), []byte("* Standup\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	put("10-16-standup.org")()
	s := newScript(t, c, p)
	got := s.run(t, put("10-17-standup.org"), put("10-17-demo.org"), put("notes.org"), put("10-17-standup.html"),
		put("10-18-brag.org"), nothing)
	want := []string{"", LineStandup + "\n", LineDemo + "\n", "", "", "", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("printed %q, want %q", got, want)
	}
	if got := s.run(t, put("10-20-standup.org")); !reflect.DeepEqual(got, []string{"", LineStandup + "\n"}) {
		t.Errorf("after a restart: %q", got)
	}
}

// With Poll, a poll line every five minutes, not at the start.
func TestWatchPoll(t *testing.T) {
	c, _, p := watchFixture(t)
	s := newScript(t, c, p)
	s.w.Poll = true
	steps := make([]func(), 21)
	for i := range steps {
		steps[i] = nothing
	}
	got := s.run(t, steps...)
	for i, line := range got {
		if want := map[int]string{10: LinePoll + "\n", 20: LinePoll + "\n"}[i]; line != want {
			t.Errorf("poll %d printed %q, want %q", i, line, want)
		}
	}
}

// One watch at a time: another says so and returns at once.
func TestWatchLock(t *testing.T) {
	c, _, p := watchFixture(t)
	held, err := tryLock(filepath.Join(p.Dir, "watch.lock"))
	if err != nil || held == nil {
		t.Fatalf("lock %v %v", held, err)
	}
	defer held.Close()
	s := newScript(t, c, p)
	s.w.Wait = func(context.Context, time.Duration) bool {
		t.Error("a second watch polled")
		return false
	}
	if err := s.w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.w.Out.(*bytes.Buffer).String(); got != LineAlready+"\n" {
		t.Errorf("printed %q", got)
	}
	if s.pulls != 0 {
		t.Errorf("%d pulls", s.pulls)
	}
}
