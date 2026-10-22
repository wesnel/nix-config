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
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/export"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/record"
)

// The lines Watch prints, each a Monitor event that wakes the dashboard
// session; the agent-plans skill says what each asks of it. They are fixed
// text, so nothing from the plans or the page reaches the session through
// them.
const (
	LineChanged = "plans changed"
	LineStalled = "plan sync stalled"
	LineDemo    = "demo digest ready"
	LineAlready = "already watching"
	// LineReviews is GitHub's view of the operator's pull requests and the
	// reviews asked of them changing, and LineRecord a review record of one
	// of them appearing or changing.
	LineReviews = "reviews changed"
	LineRecord  = "review record changed"
)

const (
	// every is how often Watch looks.
	every = 30 * time.Second
	// quiet is the least time between two LineChanged.
	quiet = 90 * time.Second
	// pullEvery is how often Watch takes the other hosts' plans.
	pullEvery = 2 * time.Minute
	// githubEvery is how often Watch reads GitHub, never under the status
	// cache's minute, which is one search query of GitHub's 5000 an hour.
	githubEvery = 5 * time.Minute
	// stallAfter is how old this host's last push may be while its plans
	// are unsynced before the sync counts as stalled.
	stallAfter = 6 * time.Hour
)

// Watch looks at the plans every 30 seconds, as the page would show them,
// and prints a line where the dashboard session has something to do. Its
// state, kept in Dir, the agent-plans state directory, outlives each run:
// Monitor stops a watch after at most 30 minutes, and the session starts
// the next.
type Watch struct {
	Dir    string
	Config plans.Config
	Now    func() time.Time
	// Wait waits d, and reports whether to look again.
	Wait func(ctx context.Context, d time.Duration) bool
	Out  io.Writer
	// Sync is Config.SyncState, and Pull Config.Pull.
	Sync func() (plans.SyncState, error)
	Pull func()
	// GitHub is the export's GitHub document as at now, through the status
	// cache; nil watches no pull request.
	GitHub func(now time.Time) (export.GitHub, error)
}

// watchState is what Watch keeps between runs: the hash of what it last
// printed LineChanged for, when, whether it printed a stall that has not
// ended, and the digests it has seen; and of GitHub, when it last read it,
// the hash of the pull requests it listed, and of each one's review
// records. Started is false until a run has recorded what it first found.
type watchState struct {
	Started bool      `json:"started"`
	Hash    string    `json:"hash"`
	Printed time.Time `json:"printed"`
	Stalled bool      `json:"stalled"`
	Digests []string  `json:"digests"`

	Read    time.Time         `json:"read"`
	Reviews string            `json:"reviews"`
	Records map[string]string `json:"records"`
}

func (w Watch) stateFile() string { return filepath.Join(w.Dir, "watch.json") }
func (w Watch) lockFile() string  { return filepath.Join(w.Dir, "watch.lock") }

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
	var pulled time.Time
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
		lines = append(lines, w.reviews(now, st, &next)...)
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
		slices.Equal(a.Digests, b.Digests) && a.Read.Equal(b.Read) && a.Reviews == b.Reviews && maps.Equal(a.Records, b.Records)
}

// reviews reads GitHub every githubEvery, and the review records of the
// pull requests it last listed at every look, into next, and returns the
// lines what changed since st asks for: LineReviews where the pull
// requests the page lists changed, and LineRecord where a record of one
// appeared or changed. A read that fails changes nothing but the time of
// the next read: retrying sooner would make a rate limit worse.
func (w Watch) reviews(now time.Time, st watchState, next *watchState) []string {
	if w.GitHub == nil {
		return nil
	}
	var lines []string
	if st.Read.IsZero() || now.Sub(st.Read) >= githubEvery || now.Before(st.Read) {
		next.Read = now
		if g, err := w.GitHub(now); err == nil && g.At != "" {
			watched := map[string]string{}
			for _, p := range g.Mine {
				watched[fmt.Sprintf("%s#%d", p.Repo, p.Number)] = ""
			}
			for _, r := range g.Requests {
				watched[fmt.Sprintf("%s#%d", r.Repo, r.Number)] = ""
			}
			// Records are LineRecord's, so the pull requests' hash leaves
			// out what their records say.
			for i := range g.Mine {
				p := &g.Mine[i]
				p.RecordExists, p.RecordFindings, p.RecordThreads, p.RecordUnanswered, p.RecordDrafted, p.RecordDiagrams =
					false, nil, 0, 0, 0, nil
			}
			for i := range g.Requests {
				r := &g.Requests[i]
				r.RecordExists, r.RecordFindings, r.RecordThreads, r.RecordUnanswered, r.RecordDrafted, r.RecordDiagrams =
					false, nil, 0, 0, 0, nil
			}
			data, _ := json.Marshal(struct {
				Mine     []export.PR
				Requests []export.Request
			}{g.Mine, g.Requests})
			sum := sha256.Sum256(data)
			next.Reviews = hex.EncodeToString(sum[:])
			if st.Started && st.Reviews != "" && next.Reviews != st.Reviews {
				lines = append(lines, LineReviews)
			}
			next.Records = watched
		}
	}
	records := map[string]string{}
	changed := false
	for pr := range next.Records {
		h := recordsHash(pr)
		records[pr] = h
		if old, ok := st.Records[pr]; st.Started && ok && old != h {
			changed = true
		}
	}
	next.Records = records
	if changed {
		lines = append(lines, LineRecord)
	}
	return lines
}

// recordsHash is the hash of pr's review records, OWNER/NAME#N: its range
// records and its threads' record, by name and content; "" for none.
func recordsHash(pr string) string {
	slug, n, _ := strings.Cut(pr, "#")
	owner, repo, _ := strings.Cut(slug, "/")
	if !record.ValidName(owner) || !record.ValidName(repo) {
		return ""
	}
	dir := filepath.Join(record.Dir(), owner, repo)
	entries, _ := os.ReadDir(dir)
	sum := sha256.New()
	found := false
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".org") || strings.HasPrefix(name, ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if name == "pr-"+n+".org" || strings.Contains(string(data), "\n:PR: "+n+"\n") {
			fmt.Fprintf(sum, "%s\x00%d\x00", name, len(data))
			sum.Write(data)
			found = true
		}
	}
	if !found {
		return ""
	}
	return hex.EncodeToString(sum.Sum(nil))
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
// YYYY/MM-DD-demo.org.
var digestFile = regexp.MustCompile(`^[0-9]{2}-[0-9]{2}-demo\.org$`)

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
			if !digestFile.MatchString(e.Name()) || !e.Type().IsRegular() {
				continue
			}
			out = append(out, digest{fmt.Sprintf("%d/%s", year, e.Name()), LineDemo})
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

// writeFile replaces file with data, 0600, by rename.
func writeFile(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), file)
}
