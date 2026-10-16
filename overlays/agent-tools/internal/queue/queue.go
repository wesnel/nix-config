// Package queue keeps the queue of reviews the operator dispatched from the
// "Agent plans" page: queue.org, which holds keys, states, heads, flags,
// times and exit statuses alone, each checked against its shape when it is
// read and written, so that nothing anyone else wrote reaches it. Dispatch
// takes the page's requests into it; Runner works through it, one review at
// a time.
package queue

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// Paths are the dashboard's state directory and what the queue keeps there.
// The systemd units name Off and the directory too.
type Paths struct{ Dir string }

// Default is $XDG_STATE_HOME/agent-plans.
func Default() Paths { return Paths{Dir: filepath.Join(hostpaths.StateHome(), "agent-plans")} }

// Out is where plan-dash export writes the page's documents, and where the
// dashboard session puts what it reads of the page's database: whatever is
// under it may have come from the page.
func (p Paths) Out() string { return filepath.Join(p.Dir, "out") }

// Listed is the review requests the last export put on the page, outside
// Out, which the session can write.
func (p Paths) Listed() string { return filepath.Join(p.Dir, "requests.json") }

// Queue is queue.org.
func (p Paths) Queue() string { return filepath.Join(p.Dir, "queue.org") }

// Off is the kill switch: while it exists, no review starts, and the queue
// unit's ConditionPathExists keeps the unit from starting.
func (p Paths) Off() string { return filepath.Join(p.Dir, "off") }

func (p Paths) lockFile() string { return filepath.Join(p.Dir, "queue.lock") }
func (p Paths) runLock() string  { return filepath.Join(p.Dir, "queue-run.lock") }

// IsOff reports whether the kill switch is on. Anything at Off counts, and
// so does an error other than its absence.
func (p Paths) IsOff() bool {
	_, err := os.Lstat(p.Off())
	return !errors.Is(err, fs.ErrNotExist)
}

// The states of an entry. Queued and Postponed wait for the runner;
// Running is the one review under way; the others are final until the
// operator asks again.
const (
	Queued        = "queued"
	Running       = "running"
	Postponed     = "postponed"
	NeedsOperator = "needs-operator"
	Ready         = "ready"
	Failed        = "failed"
	// Stale is an entry whose head the page no longer lists, or GitHub no
	// longer has: it is not reviewed until asked for again.
	Stale = "stale"
)

var states = []string{Queued, Running, Postponed, NeedsOperator, Ready, Failed, Stale}

// Final reports whether state waits on nothing but the operator.
func Final(state string) bool {
	return state == NeedsOperator || state == Ready || state == Failed || state == Stale
}

// NoExit is an exit status not had yet.
const NoExit = -1

// Entry is one pull request in the queue. Head is the head the page listed
// when it was asked for, the only one it is reviewed at.
type Entry struct {
	Key    string
	State  string
	Head   string
	Triage bool
	// Anyway is the operator's "run anyway": the quota gate passes it on
	// its next run alone.
	Anyway bool
	// Restarted is whether a runner was stopped while it ran this entry,
	// which is being run again.
	Restarted bool
	Queued    time.Time
	Changed   time.Time
	// Until is when a postponed entry's quota window resets.
	Until time.Time
	// Review and Triaged are the exit statuses of `pr-loop review` and of
	// the triage session, or NoExit.
	Review  int
	Triaged int
}

var (
	// key is OWNER/NAME#N, neither part starting with a dot, so that it
	// names no path above where it is joined.
	key      = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_.-]{0,99})/([A-Za-z0-9_][A-Za-z0-9_.-]{0,99})#([1-9][0-9]{0,9})$`)
	fullHead = regexp.MustCompile(`^[0-9a-f]{40}$`)
	headline = regexp.MustCompile(`^\* ([A-Z-]+) (\S+)$`)
	property = regexp.MustCompile(`^:([A-Z]+):(?: (.*))?$`)
	exitCode = regexp.MustCompile(`^[0-9]{1,3}$`)
)

// IsKey reports whether k is OWNER/NAME#N.
func IsKey(k string) bool { return key.MatchString(k) }

// Split is k's OWNER/NAME and N.
func Split(k string) (string, int) {
	m := key.FindStringSubmatch(k)
	if m == nil {
		return "", 0
	}
	n, _ := strconv.Atoi(m[3])
	return m[1] + "/" + m[2], n
}

// valid is why e cannot be written, or "".
func (e Entry) valid() string {
	switch {
	case !key.MatchString(e.Key):
		return "a key that is not OWNER/NAME#N"
	case !known(e.State):
		return "an unknown state"
	case e.Head != "" && !fullHead.MatchString(e.Head):
		return "a head that is not a commit ID"
	case e.Review < NoExit || e.Review > 255 || e.Triaged < NoExit || e.Triaged > 255:
		return "an exit status out of range"
	}
	return ""
}

func known(state string) bool {
	for _, s := range states {
		if s == state {
			return true
		}
	}
	return false
}

// Load reads queue.org: no file is an empty queue. A line it does not
// expect fails the whole read, so the runner never acts on a queue it
// misread.
func Load(p Paths) ([]Entry, error) {
	f, err := os.Open(p.Queue())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	var e *Entry
	in, done := false, false
	lines := bufio.NewScanner(f)
	n := 0
	bad := func(why string) error { return fmt.Errorf("%s:%d: %s", p.Queue(), n, why) }
	for lines.Scan() {
		n++
		line := lines.Text()
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			if e != nil && in {
				return nil, bad("a blank or comment line in a property drawer")
			}
		case strings.HasPrefix(line, "* "):
			if e != nil && !done {
				return nil, bad("an entry without its properties")
			}
			m := headline.FindStringSubmatch(line)
			if m == nil {
				return nil, bad("not an entry's headline")
			}
			out = append(out, Entry{Key: m[2], State: strings.ToLower(m[1]), Review: NoExit, Triaged: NoExit})
			e, in, done = &out[len(out)-1], false, false
		case e == nil || done:
			return nil, bad("a line outside an entry's properties")
		case line == ":PROPERTIES:" && !in:
			in = true
		case line == ":END:" && in:
			in, done = false, true
		case in:
			m := property.FindStringSubmatch(line)
			if m == nil {
				return nil, bad("not a property")
			}
			if err := e.set(m[1], m[2]); err != nil {
				return nil, bad(err.Error())
			}
		default:
			return nil, bad("a line outside an entry's properties")
		}
	}
	if err := lines.Err(); err != nil {
		return nil, err
	}
	if e != nil && !done {
		return nil, bad("an entry without its properties")
	}
	seen := map[string]bool{}
	for _, e := range out {
		if why := e.valid(); why != "" {
			return nil, fmt.Errorf("%s: %s: %s", p.Queue(), e.Key, why)
		}
		if seen[e.Key] {
			return nil, fmt.Errorf("%s: %s is queued twice", p.Queue(), e.Key)
		}
		seen[e.Key] = true
	}
	return out, nil
}

func (e *Entry) set(name, value string) error {
	var err error
	switch name {
	case "HEAD":
		e.Head = value
	case "TRIAGE":
		e.Triage, err = flag(value)
	case "ANYWAY":
		e.Anyway, err = flag(value)
	case "RESTARTED":
		e.Restarted, err = flag(value)
	case "QUEUED":
		e.Queued, err = parseTime(value)
	case "CHANGED":
		e.Changed, err = parseTime(value)
	case "UNTIL":
		e.Until, err = parseTime(value)
	case "REVIEW":
		e.Review, err = parseExit(value)
	case "TRIAGED":
		e.Triaged, err = parseExit(value)
	default:
		err = fmt.Errorf("an unknown property %s", name)
	}
	return err
}

func flag(v string) (bool, error) {
	switch v {
	case "t":
		return true, nil
	case "nil":
		return false, nil
	}
	return false, fmt.Errorf("%q is not t or nil", v)
}

func parseTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, v)
}

func parseExit(v string) (int, error) {
	if v == "" {
		return NoExit, nil
	}
	if !exitCode.MatchString(v) {
		return 0, fmt.Errorf("%q is not an exit status", v)
	}
	return strconv.Atoi(v)
}

// Save replaces queue.org with entries, by renaming a new file over it.
func Save(p Paths, entries []Entry) error {
	var b strings.Builder
	b.WriteString("#+title: Review queue\n#+todo: QUEUED RUNNING POSTPONED | NEEDS-OPERATOR READY FAILED STALE\n" +
		"# Written by plan-dash dispatch and plan-dash queue, under queue.lock; edits are lost.\n")
	for _, e := range entries {
		if why := e.valid(); why != "" {
			return fmt.Errorf("%s: %s", e.Key, why)
		}
		fmt.Fprintf(&b, "\n* %s %s\n:PROPERTIES:\n", strings.ToUpper(e.State), e.Key)
		for _, kv := range [][2]string{{"HEAD", e.Head}, {"TRIAGE", orgFlag(e.Triage)}, {"ANYWAY", orgFlag(e.Anyway)},
			{"RESTARTED", orgFlag(e.Restarted)},
			{"QUEUED", stamp(e.Queued)}, {"CHANGED", stamp(e.Changed)}, {"UNTIL", stamp(e.Until)},
			{"REVIEW", orgExit(e.Review)}, {"TRIAGED", orgExit(e.Triaged)}} {
			if kv[1] == "" {
				fmt.Fprintf(&b, ":%s:\n", kv[0])
			} else {
				fmt.Fprintf(&b, ":%s: %s\n", kv[0], kv[1])
			}
		}
		b.WriteString(":END:\n")
	}
	return writeFile(p.Queue(), []byte(b.String()))
}

func orgFlag(v bool) string {
	if v {
		return "t"
	}
	return "nil"
}

func orgExit(v int) string {
	if v == NoExit {
		return ""
	}
	return strconv.Itoa(v)
}

// stamp is t in RFC 3339, or "" for the zero time.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// writeFile replaces file with data, 0600, by rename, so a reader sees the
// old file or the new one.
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

// lock takes file's lock, waiting for it unless try; it returns nil when
// try finds it held.
func lock(file string, try bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if try {
		how |= syscall.LOCK_NB
	}
	for {
		err = syscall.Flock(int(f.Fd()), how)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		f.Close()
		if try && errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}

// update loads the queue under its lock, lets change edit it, and saves it
// when change returns true.
func update(p Paths, change func([]Entry) ([]Entry, bool, error)) ([]Entry, error) {
	l, err := lock(p.lockFile(), false)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	entries, err := Load(p)
	if err != nil {
		return nil, err
	}
	entries, save, err := change(entries)
	if err != nil || !save {
		return entries, err
	}
	return entries, Save(p, entries)
}
