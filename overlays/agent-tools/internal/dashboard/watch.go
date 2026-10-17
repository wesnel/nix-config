package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/export"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/queue"
)

// The lines Watch prints, each a Monitor event that wakes the dashboard
// session; the agent-plans skill says what each asks of it. They are fixed
// text, so nothing from the plans or the page reaches the session through
// them.
const (
	LineChanged = "plans changed"
	LineStalled = "plan sync stalled"
	LineStandup = "standup digest ready"
	LineDemo    = "demo digest ready"
	LinePoll    = "poll"
	LineAlready = "already watching"
)

const (
	// every is how often Watch looks.
	every = 30 * time.Second
	// quiet is the least time between two LineChanged.
	quiet = 90 * time.Second
	// pullEvery is how often Watch takes the other hosts' plans.
	pullEvery = 10 * time.Minute
	// pollEvery is how often Watch prints LinePoll, with Poll.
	pollEvery = 5 * time.Minute
	// stallAfter is how old this host's last push may be while its plans
	// are unsynced before the sync counts as stalled.
	stallAfter = 6 * time.Hour
)

// Watch looks at the plans every 30 seconds, as the page would show them,
// and prints a line where the dashboard session has something to do. Its
// state, kept beside the queue, outlives each run: Monitor stops a watch
// after at most 30 minutes, and the session starts the next.
type Watch struct {
	Paths  queue.Paths
	Config plans.Config
	Now    func() time.Time
	// Wait waits d, and reports whether to look again.
	Wait func(ctx context.Context, d time.Duration) bool
	Out  io.Writer
	// Poll prints LinePoll every five minutes, for a session the page's
	// comments do not wake.
	Poll bool
	// Sync is Config.SyncState, and Pull Config.Pull.
	Sync func() (plans.SyncState, error)
	Pull func()
}

// watchState is what Watch keeps between runs: the hash of what it last
// printed LineChanged for, when, whether it printed a stall that has not
// ended, and the digests it has seen. Started is false until a run has
// recorded what it first found.
type watchState struct {
	Started bool      `json:"started"`
	Hash    string    `json:"hash"`
	Printed time.Time `json:"printed"`
	Stalled bool      `json:"stalled"`
	Digests []string  `json:"digests"`
}

func (w Watch) stateFile() string { return filepath.Join(w.Paths.Dir, "watch.json") }
func (w Watch) lockFile() string  { return filepath.Join(w.Paths.Dir, "watch.lock") }

// Run watches until ctx ends or Wait says to stop. Another run already
// watching makes it print LineAlready and return.
func (w Watch) Run(ctx context.Context) error {
	l, err := tryLock(w.lockFile())
	if err != nil {
		return err
	}
	if l == nil {
		_, err := fmt.Fprintln(w.Out, LineAlready)
		return err
	}
	defer l.Close()
	var st watchState
	if data, err := os.ReadFile(w.stateFile()); err == nil {
		// A state that does not parse starts again, printing nothing for
		// what it finds.
		if json.Unmarshal(data, &st) != nil {
			st = watchState{}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	start := w.Now()
	var pulled time.Time
	polled := start
	for {
		now := w.Now()
		if pulled.IsZero() || now.Sub(pulled) >= pullEvery {
			w.Pull()
			pulled = now
		}
		var lines []string
		next := st
		hash, err := w.hash(now)
		if err == nil && hash != st.Hash && (!st.Started || now.Sub(st.Printed) >= quiet) {
			next.Hash = hash
			if st.Started {
				lines, next.Printed = append(lines, LineChanged), now
			}
		}
		if s, err := w.Sync(); err == nil {
			stalled := s.Repo && s.Unsynced && !s.Pushed.IsZero() && now.Sub(s.Pushed) > stallAfter
			if stalled && !st.Stalled {
				lines = append(lines, LineStalled)
			}
			next.Stalled = stalled
		}
		digests := w.digests(now)
		for _, d := range digests {
			if st.Started && !slices.Contains(st.Digests, d.name) {
				lines = append(lines, d.line)
			}
		}
		next.Digests = next.Digests[:0:0]
		for _, d := range digests {
			next.Digests = append(next.Digests, d.name)
		}
		if w.Poll && now.Sub(polled) >= pollEvery {
			lines, polled = append(lines, LinePoll), now
		}
		next.Started = true
		if !sameState(st, next) {
			data, err := json.Marshal(next)
			if err != nil {
				return err
			}
			if err := writeFile(w.stateFile(), data); err != nil {
				return err
			}
		}
		st = next
		for _, line := range lines {
			if _, err := fmt.Fprintln(w.Out, line); err != nil {
				return err
			}
		}
		if !w.Wait(ctx, every) {
			return nil
		}
	}
}

// sameState reports whether a and b are the same state.
func sameState(a, b watchState) bool {
	return a.Started == b.Started && a.Hash == b.Hash && a.Printed.Equal(b.Printed) && a.Stalled == b.Stalled &&
		slices.Equal(a.Digests, b.Digests)
}

// hash is the hash of what an export at now would put on the page of the
// plans and their sync, without what changes when nothing the page shows
// does: when each plan's log last changed, when the export ran, and when
// this host last pushed or fetched.
func (w Watch) hash(now time.Time) (string, error) {
	ps, failed, err := export.Plans(w.Config, now)
	if ps == nil {
		return "", err
	}
	for i := range ps {
		ps[i].Updated = ""
	}
	s, err := w.Sync()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		Plans    []export.Plan
		Failed   []string
		Repo     bool
		Unsynced bool
	}{ps, failed, s.Repo, s.Unsynced})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// digestFile is a digest's file under digests/ in the notes repository:
// YYYY/MM-DD-standup.org or YYYY/MM-DD-demo.org.
var digestFile = regexp.MustCompile(`^[0-9]{2}-[0-9]{2}-(standup|demo)\.org$`)

type digest struct{ name, line string }

// digests are the digests in the notes repository of now's year and the
// one before, by name.
func (w Watch) digests(now time.Time) []digest {
	repo := w.Config.Repo
	if repo == "" {
		repo = filepath.Dir(w.Config.Dir)
	}
	var out []digest
	for _, year := range []int{now.Year() - 1, now.Year()} {
		dir := filepath.Join(repo, "digests", fmt.Sprint(year))
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			m := digestFile.FindStringSubmatch(e.Name())
			if m == nil || !e.Type().IsRegular() {
				continue
			}
			line := LineStandup
			if m[1] == "demo" {
				line = LineDemo
			}
			out = append(out, digest{fmt.Sprintf("%d/%s", year, e.Name()), line})
		}
	}
	return out
}

// tryLock takes file's lock, or returns nil where another holds it.
func tryLock(file string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}
