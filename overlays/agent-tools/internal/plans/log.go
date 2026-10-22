package plans

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// Event is one entry of a plan's log.
type Event struct {
	ID       string `json:"event"`
	Time     string `json:"time"`
	Kind     string `json:"kind"`
	Item     string `json:"item"`
	Summary  string `json:"summary,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Role     string `json:"role,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Review   string `json:"review,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	// Note is what the caller should know about the event, such as a
	// heading keyword Log could not write; the log does not hold it.
	Note string `json:"note,omitempty"`
}

var (
	// Kinds are what an event records about its item.
	Kinds = []string{"planned", "plan-reviewed", "started", "done", "blocked", "abandoned", "verified",
		"reviewed", "finding-settled", "asked", "decided", "handed-back", "committed", "pushed", "postponed", "discovered", "referenced"}
	// Roles are who logs an event.
	Roles = []string{"single", "parent", "subagent", "reviewer", "design-reviewer"}
	// Outcomes are the review-change skill's triage outcomes.
	Outcomes = []string{"fix", "rebut", "duplicate-of", "belongs-in", "defer", "needs-wesley"}

	reviewPattern = regexp.MustCompile(`^[0-9]+\.[A-Za-z0-9_-]+$`)
)

func oneOf(value string, allowed []string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	return false
}

// check refuses an event the log must not hold.
func (p Plan) check(e Event) error {
	for what, v := range map[string]string{"summary": e.Summary, "agent": e.Agent, "branch": e.Branch,
		"commit": e.Commit, "evidence": e.Evidence, "item": e.Item, "kind": e.Kind} {
		if err := checkLine(what, v); err != nil {
			return err
		}
	}
	switch {
	case !oneOf(e.Kind, Kinds):
		return refusal.New("%q is not an event kind. Use one of `%s`", e.Kind, strings.Join(Kinds, "`, `"))
	case e.Role != "" && !oneOf(e.Role, Roles):
		return refusal.New("%q is not a role. Use one of `%s`", e.Role, strings.Join(Roles, "`, `"))
	case e.Outcome != "" && !oneOf(e.Outcome, Outcomes):
		return refusal.New("%q is not a triage outcome. Use one of `%s`, as the `review-change` skill defines them", e.Outcome, strings.Join(Outcomes, "`, `"))
	case e.Review != "" && !reviewPattern.MatchString(e.Review):
		return refusal.New("review %q is not <round>.<finding id>. Pass it as `--review 1.F3`", e.Review)
	case e.Kind == "finding-settled" && (e.Review == "" || e.Outcome == ""):
		return refusal.New("a settled finding needs the finding and its outcome. Pass `--review <round>.<id> --outcome <outcome>`")
	case strings.ContainsAny(e.Agent+e.Branch, " \t"):
		return refusal.New("agent and branch are single words. Pass them without spaces, as `--agent reviewer`")
	}
	items, err := p.items()
	if err != nil {
		return err
	}
	if _, ok := items[e.Item]; !ok {
		return refusal.New("%q is not an item of %s. Add it to the plan file first, or use an ID `agent-plan status %s` lists", e.Item, p.Name, p.file(""))
	}
	return nil
}

// Log appends e to the plan's log, with a new event ID and now's time, and
// returns it as written. A step's or part's heading keyword then follows
// the log: the log is the record, and the keyword its projection for
// Emacs. A keyword it cannot write leaves the event logged, says so in the
// returned event's Note, and shows in status as drift.
func (p Plan) Log(e Event, now time.Time) (Event, error) {
	if err := p.checkOwn(); err != nil {
		return Event{}, err
	}
	err := p.withLock(func(l *lockedPlan) error {
		var err error
		if e, err = l.log(e, now); err != nil {
			return err
		}
		if err := l.project(e.Item, now); err != nil {
			e.Note = fmt.Sprintf("the event is logged, but not %s's heading keyword: %v", e.Item, err)
		}
		return nil
	})
	return e, err
}

// projected reports whether id's heading keyword is the log's projection:
// a step's, a part's or a follow-up's.
func projected(id string) bool {
	return stepPattern.MatchString(id) || partPattern.MatchString(id) || followupID.MatchString(id)
}

// project writes the keyword the log gives id into its heading, unless it
// is there, and updates the parent heading's statistics cookie as Org
// does.
func (l *lockedPlan) project(id string, now time.Time) error {
	if !projected(id) {
		return nil
	}
	events, err := l.p.Events()
	if err != nil {
		return err
	}
	items, err := l.p.items()
	if err != nil {
		return err
	}
	it, ok := items[id]
	if !ok || it.Keyword == stepState(events, id) {
		return nil
	}
	state := stepState(events, id)
	return l.edit(it.File, func(lines []string) ([]string, error) {
		at := headingWithID(lines, id)
		ks := fileKeywords(strings.Join(lines, "\n"))
		if at < 0 || !ks.all[state] {
			return lines, nil
		}
		lines = setKeyword(lines, at, state, ks, now)
		return updateCookie(lines, at, ks), nil
	})
}

// log is Log under the lock.
func (l *lockedPlan) log(e Event, now time.Time) (Event, error) {
	if err := l.p.check(e); err != nil {
		return Event{}, err
	}
	e.ID, e.Time = newUUID(), now.Format("[2006-01-02 Mon 15:04]")
	return e, l.append(eventText(e))
}

// Reseal accepts a log changed other than through Log: it appends a
// resealed event recording the digest the log had and the one it has, and
// records the log as it then is. It is Wesley's to run.
func (p Plan) Reseal(now time.Time) (Event, error) {
	if err := p.checkOwn(); err != nil {
		return Event{}, err
	}
	e := Event{ID: newUUID(), Time: now.Format("[2006-01-02 Mon 15:04]"), Kind: "resealed", Item: p.Name,
		Summary: "log edited outside agent-plan"}
	return e, p.withLock(func(l *lockedPlan) error {
		return l.appendWith(func(current []byte, recorded string) (string, error) {
			if recorded == "" || recorded == digest(current) {
				return "", refusal.New("%s is as agent-plan last left it, so there is nothing to reseal. Check it with `agent-plan status %s`", l.p.log(), l.p.file(""))
			}
			e.Evidence = "recorded " + recorded + "; found " + digest(current)
			return eventText(e), nil
		})
	})
}

// eventText is e as the log holds it.
func eventText(e Event) string {
	var b strings.Builder
	b.WriteString("* " + e.Kind + " " + e.Item)
	if e.Summary != "" {
		b.WriteString(" — " + e.Summary)
	}
	b.WriteString("\n:PROPERTIES:\n")
	for _, kv := range [][2]string{{"EVENT", e.ID}, {"AGENT", e.Agent}, {"ROLE", e.Role}, {"ITEM", e.Item},
		{"KIND", e.Kind}, {"BRANCH", e.Branch}, {"COMMIT", e.Commit}, {"EVIDENCE", e.Evidence},
		{"REVIEW", e.Review}, {"OUTCOME", e.Outcome}} {
		if kv[1] != "" {
			b.WriteString(":" + kv[0] + ": " + kv[1] + "\n")
		}
	}
	// The drawer comes straight after the heading, or Org does not read
	// it as one.
	b.WriteString(":END:\n" + e.Time + "\n")
	return b.String()
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// lockedPlan is a plan whose log's lock this process holds: every change
// to the plan's files goes through one, so that agent-plan calls running
// at once neither lose a write nor reuse an ID.
type lockedPlan struct {
	p    Plan
	lock *os.File
}

// withLock runs fn holding an exclusive flock on the log's lock file. It
// is the only way to change a plan's files, and never nests: a flock
// belongs to an open file description, so a nested withLock waits for
// itself.
func (p Plan) withLock(fn func(l *lockedPlan) error) error {
	lock, err := os.OpenFile(p.log()+".lock", os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn(&lockedPlan{p: p, lock: lock})
}

// append adds text to the log in one write and syncs it. The lock file
// records the log's size and digest after each append; a log that no
// longer matches them was changed some other way, and is refused.
func (l *lockedPlan) append(text string) error {
	return l.appendWith(func(current []byte, recorded string) (string, error) {
		if recorded != "" && recorded != digest(current) {
			return "", refusal.New("%s has changed since agent-plan last appended to it, and the log is append-only. Leave it as it is, and tell Wesley which plan's log changed; `agent-plan reseal` is his to run", l.p.log())
		}
		return text, nil
	})
}

// appendWith appends what build returns, given the log and the digest
// recorded for it, as append does, and records the log's new digest.
func (l *lockedPlan) appendWith(build func(current []byte, recorded string) (string, error)) error {
	recorded := make([]byte, 256)
	n, err := l.lock.ReadAt(recorded, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	log, err := os.OpenFile(l.p.log(), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer log.Close()
	current, err := io.ReadAll(log)
	if err != nil {
		return err
	}
	text, err := build(current, strings.TrimSpace(string(recorded[:n])))
	if err != nil {
		return err
	}
	if len(current) > 0 && current[len(current)-1] != '\n' {
		text = "\n" + text
	}
	if n, err := log.Write([]byte(text)); err != nil || n != len(text) {
		return fmt.Errorf("appending to %s: wrote %d of %d bytes: %v", l.p.log(), n, len(text), err)
	}
	if err := log.Sync(); err != nil {
		return err
	}
	if err := l.lock.Truncate(0); err != nil {
		return err
	}
	if _, err := l.lock.WriteAt([]byte(digest(append(current, text...))+"\n"), 0); err != nil {
		return err
	}
	if err := l.lock.Sync(); err != nil {
		return err
	}
	touch(l.p.Dir)
	return nil
}

// editTries is how many times edit reads a file that changes under it
// before refusing.
const editTries = 3

// edit is the one rewrite of a plan file: it applies fn, which must be
// pure over the lines, and renames the result over the file. A result
// equal to the file is not written. The lock excludes other agent-plan
// processes but not Emacs, so edit refuses a file whose buffer has unsaved
// changes, and applies fn again to a file that changed between its read
// and the rename. The window between that last check and the rename stays
// open.
func (l *lockedPlan) edit(file string, fn func(lines []string) ([]string, error)) error {
	emacsLock := filepath.Join(filepath.Dir(file), ".#"+filepath.Base(file))
	if info, err := os.Lstat(emacsLock); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return refusal.New("%s has unsaved changes in an Emacs buffer (%s exists), which would overwrite this change when saved. Ask Wesley to save or revert that buffer, then run the command again", file, emacsLock)
	}
	for range editTries {
		before, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		lines, err := fn(strings.Split(string(before), "\n"))
		if err != nil {
			return err
		}
		text := strings.Join(lines, "\n")
		if text == string(before) {
			return nil
		}
		err = replaceFile(file, text, func() bool {
			now, err := os.ReadFile(file)
			return err == nil && bytes.Equal(now, before)
		})
		if !errors.Is(err, errChanged) {
			return err
		}
	}
	return refusal.New("%s kept changing while agent-plan edited it. Ask Wesley what else is writing it, then run the command again", file)
}

// errChanged is replaceFile's error for a file that changed before the
// rename.
var errChanged = errors.New("changed before the rename")

// replaceFile writes text to path by renaming a new file over it, if
// unchanged, asked just before the rename, reports path as it was read.
func replaceFile(path, text string, unchanged func() bool) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".edit-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if !unchanged() {
		return errChanged
	}
	return os.Rename(f.Name(), path)
}

// touch changes the directory's entries and leaves them as they were, so
// that a watcher that sees only a directory's entries change, as launchd's
// WatchPaths does, sees a file in it written in place.
func touch(dir string) {
	if f, err := os.CreateTemp(dir, ".touch-*"); err == nil {
		f.Close()
		os.Remove(f.Name())
	}
}

func digest(data []byte) string {
	return fmt.Sprintf("%d %x", len(data), sha256.Sum256(data))
}

// Events reads the plan's log, oldest first.
func (p Plan) Events() ([]Event, error) {
	data, err := os.ReadFile(p.log())
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, h := range parseOrg(string(data)) {
		if h.Props["EVENT"] == "" {
			continue
		}
		e := Event{ID: h.Props["EVENT"], Kind: h.Props["KIND"], Item: h.Props["ITEM"], Agent: h.Props["AGENT"],
			Role: h.Props["ROLE"], Branch: h.Props["BRANCH"], Commit: h.Props["COMMIT"],
			Evidence: h.Props["EVIDENCE"], Review: h.Props["REVIEW"], Outcome: h.Props["OUTCOME"]}
		if _, summary, ok := strings.Cut(h.Title, " — "); ok {
			e.Summary = summary
		}
		for _, line := range h.Body {
			if strings.HasPrefix(line, "[") {
				e.Time = strings.TrimSpace(line)
				break
			}
		}
		out = append(out, e)
	}
	return out, nil
}
