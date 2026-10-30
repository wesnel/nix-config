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
	ID          string `json:"event"`
	Time        string `json:"time"`
	Kind        string `json:"kind"`
	Item        string `json:"item"`
	Summary     string `json:"summary,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Role        string `json:"role,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Repository  string `json:"repository,omitempty"`
	Revision    string `json:"revision,omitempty"`
	OldRevision string `json:"old_revision,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
	Review      string `json:"review,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
	// Session is the session whose tool call the event records, as its
	// hook names it: Claude Code's or Codex's session_id.
	Session string `json:"session,omitempty"`
	// Worktree is the top level of the worktree the event happened in, and
	// Tip, on a branch-changed event, the commit of a detached HEAD.
	Worktree string `json:"worktree,omitempty"`
	Tip      string `json:"tip,omitempty"`
	// Sign is a sign-pass's ID, Exit its exit status, and Delivers the
	// event ID of the result a sign-delivered event delivered.
	Sign     string `json:"sign,omitempty"`
	Exit     string `json:"exit,omitempty"`
	Delivers string `json:"delivers,omitempty"`
	// Property and Value are a property event's: the property of the
	// item's `* Plan` heading, and its values, separated by spaces.
	Property string `json:"property,omitempty"`
	Value    string `json:"value,omitempty"`
	// Command is a handed-back event's command for the operator to run,
	// and Dir the directory to run it in; Waiting reads them.
	Command string `json:"command,omitempty"`
	Dir     string `json:"dir,omitempty"`
	// Base is a sign-requested event's base for git sign-pass --base, ""
	// for main.
	Base string `json:"base,omitempty"`
	// Note is what the caller should know about the event, such as a
	// heading keyword Log could not write; the log does not hold it.
	Note string `json:"note,omitempty"`
}

var (
	// Kinds are what an event an agent logs records about its item;
	// toolKinds are beside them.
	Kinds = []string{"planned", "plan-reviewed", "started", "done", "blocked", "abandoned", "verified",
		"reviewed", "finding-settled", "asked", "decided", "handed-back", "postponed", "discovered"}
	// Roles are who logs an event.
	Roles = []string{"single", "parent", "subagent", "reviewer", "design-reviewer"}
	// Outcomes are the review-change skill's triage outcomes; needs-operator
	// is a finding left for the operator to decide. They are stored in plan
	// logs, so renaming one means rewriting every log that holds it.
	Outcomes = []string{"fix", "rebut", "duplicate-of", "belongs-in", "defer", "needs-operator"}

	reviewPattern = regexp.MustCompile(`^[0-9]+\.[A-Za-z0-9_-]+$`)

	// toolKinds are the kinds only agent-tools' own commands and hooks
	// log, through Record, from what they saw happen; each maps to the
	// refusal Log gives an agent that logs one.
	toolKinds = func() map[string]string {
		kinds := map[string]string{
			"commit-rewritten": "git sign-pass records exact rewrite aliases; log an association with --revision REF",
			"committed": "a hook after each shell call logs `committed` for every commit, from what git printed. " +
				"Log `done` with `--revision REF` in its worktree for the step a commit completes",
			propertyKind: "agent-plan logs each change of a plan's WORKTREES, BRANCHES, PULL_REQUESTS and OWNERS that it makes. " +
				"Change one with `agent-plan set PLAN NAME VALUE...`, or OWNERS with `agent-plan own`",
			branchChanged: "the guard logs `branch-changed` when a session's file edit lands on another branch or worktree than its last. " +
				"Confirm the branch with `git branch --show-current` before the edit",
			"pushed": "pr-loop, and a hook after each shell call, log `pushed` for every push, from what they pushed. " +
				"Watch the push's CI with `pr-loop wait ci` as the signing-pass skill says",
			linkedKind: "agent-plan link logs `linked` for each link it writes under Related. " +
				"Link a plan with `agent-plan link PLAN OTHER --why TEXT`",
			"referenced": "agent-plan reference logs `referenced` for each URL it writes under References, and status reads its summary as that URL. " +
				"Add one with `agent-plan reference PLAN URL`",
		}
		for _, k := range []string{"loop-began", "round", "replied", "resolved", "ci-waited"} {
			kinds[k] = "pr-loop logs what it did, from begin on, in the plan of the session that ran it. " +
				"Run the `pr-loop` command that does it"
		}
		for _, k := range signKinds {
			kinds[k] = "a sign-pass's events are logged by `agent-plan sign-id`, `git sign-pass --notify` and the delivery of its result alone. " +
				"Hand the operator the sign-pass as the signing-pass skill says"
		}
		return kinds
	}()
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
		"commit": e.Commit, "evidence": e.Evidence, "item": e.Item, "kind": e.Kind, "session": e.Session,
		"worktree": e.Worktree, "tip": e.Tip, "property": e.Property, "value": e.Value} {
		if err := checkLine(what, v); err != nil {
			return err
		}
	}
	if e.Revision != "" || e.Repository != "" || e.OldRevision != "" {
		if !repositoryPattern.MatchString(e.Repository) || !revisionPattern.MatchString(e.Revision) {
			return refusal.New("a commit identity needs a repository owner/name and full revision")
		}
		if e.OldRevision != "" && !revisionPattern.MatchString(e.OldRevision) {
			return refusal.New("a rewritten commit needs its full original revision")
		}
		if e.Kind != "committed" && e.Kind != "done" && e.Kind != "verified" && e.Kind != "commit-rewritten" {
			return refusal.New("commit identities belong to committed, done, verified or commit-rewritten events")
		}
		if (e.Kind == "commit-rewritten") != (e.OldRevision != "") {
			return refusal.New("only a rewrite event carries an original revision")
		}
	}
	for what, v := range map[string]string{"command": e.Command, "dir": e.Dir, "base": e.Base} {
		if err := checkLine(what, v); err != nil {
			return err
		}
	}
	switch {
	case (e.Command != "" || e.Dir != "") && e.Kind != handedBack:
		return refusal.New("--command and --dir name what the operator runs after a hand-back. Log them with `--kind %s`", handedBack)
	case e.Dir != "" && e.Command == "":
		return refusal.New("--dir is where the operator runs --command. Pass the command too, as `--command 'git sign-pass --push' --dir DIR`")
	case e.Dir != "" && !filepath.IsAbs(e.Dir):
		return refusal.New("--dir %q is not absolute, and the operator runs it from anywhere. Pass the full path, as `--dir /path/to/worktree`", e.Dir)
	}
	switch {
	case !oneOf(e.Kind, Kinds) && toolKinds[e.Kind] == "":
		return refusal.New("%q is not an event kind. Use one of `%s`", e.Kind, strings.Join(Kinds, "`, `"))
	case e.Role != "" && !oneOf(e.Role, Roles):
		return refusal.New("%q is not a role. Use one of `%s`", e.Role, strings.Join(Roles, "`, `"))
	case e.Outcome != "" && !oneOf(e.Outcome, Outcomes):
		return refusal.New("%q is not a triage outcome. Use one of `%s`, as the `review-change` skill defines them", e.Outcome, strings.Join(Outcomes, "`, `"))
	case e.Review != "" && !reviewPattern.MatchString(e.Review):
		return refusal.New("review %q is not <round>.<finding id>. Pass it as `--review 1.F3`", e.Review)
	case e.Kind == "finding-settled" && (e.Review == "" || e.Outcome == ""):
		return refusal.New("a settled finding needs the finding and its outcome. Pass `--review <round>.<id> --outcome <outcome>`")
	case strings.ContainsAny(e.Agent+e.Branch+e.Session, " \t"):
		return refusal.New("agent, branch and session are single words. Pass them without spaces, as `--agent reviewer`")
	}
	items, err := p.items()
	if err != nil {
		return err
	}
	if _, ok := items[e.Item]; !ok {
		return refusal.New("%q is not an item of %s. Add it first with `agent-plan step` or `agent-plan part`, or use an ID `agent-plan status %s` lists", e.Item, p.Name, p.file(""))
	}
	return nil
}

// Log appends e, an event an agent logs, to the plan's log, with a new
// event ID and now's time, and returns it as written; it refuses the kinds
// only tools log. A step's or part's heading keyword then follows the log:
// the log is the record, and the keyword its projection for Emacs. A
// keyword it cannot write leaves the event logged, says so in the returned
// event's Note, and shows in status as drift.
func (p Plan) Log(e Event, now time.Time) (Event, error) {
	if why := toolKinds[e.Kind]; why != "" {
		return Event{}, refusal.New("%s", why)
	}
	return p.record(e, now)
}

// record is Log for any kind, the tools' too.
func (p Plan) record(e Event, now time.Time) (Event, error) {
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
// records the log as it then is. It is the operator's to run.
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
		{"REVIEW", e.Review}, {"OUTCOME", e.Outcome}, {"SESSION", e.Session}, {"WORKTREE", e.Worktree}, {"TIP", e.Tip},
		{"SIGN", e.Sign}, {"EXIT", e.Exit}, {"DELIVERS", e.Delivers}, {"PROPERTY", e.Property}, {"VALUE", e.Value},
		{"COMMAND", e.Command}, {"DIR", e.Dir}, {"BASE", e.Base}} {
		if kv[1] != "" {
			b.WriteString(":" + kv[0] + ": " + kv[1] + "\n")
		}
	}
	for _, kv := range [][2]string{{"REPOSITORY", e.Repository}, {"REVISION", e.Revision}, {"OLD_REVISION", e.OldRevision}} {
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
	// originals are the plan files edit has rewritten, as they were;
	// created the ones create made; written each file as either last left
	// it; appended is whether an event reached the log. withLock restores
	// the originals, and removes the created, when its function fails
	// before an event records the change.
	originals, written map[string][]byte
	created            []string
	appended           bool
}

// withLock runs fn holding an exclusive flock on the log's lock file. It
// is the only way to change a plan's files, and never nests: a flock
// belongs to an open file description, so a nested withLock waits for
// itself.
func (p Plan) withLock(fn func(l *lockedPlan) error) error {
	lock, err := os.OpenFile(p.log()+".lock", os.O_RDWR|os.O_CREATE, 0o644) //nolint:gosec // an empty lock file
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
	l := &lockedPlan{p: p, lock: lock, originals: map[string][]byte{}, written: map[string][]byte{}}
	err = fn(l)
	// A command refused after it rewrote a plan file, and before any event
	// recorded that, leaves the file as it found it: run again, it would
	// write the change twice. Once an event is logged, the file stays as
	// the event describes, and a file someone saved since stays theirs.
	if err != nil && !l.appended {
		for file, text := range l.originals {
			replaceFile(file, string(text), func() bool {
				now, err := os.ReadFile(file)
				return err == nil && bytes.Equal(now, l.written[file])
			})
		}
		for _, file := range l.created {
			if now, err := os.ReadFile(file); err == nil && bytes.Equal(now, l.written[file]) {
				os.Remove(file)
			}
		}
	}
	return err
}

// create writes the plan file file, which must not exist yet, as
// writeNew does, for withLock to remove again if the command fails
// before an event records it.
func (l *lockedPlan) create(file, text string) error {
	if err := writeNew(file, text); err != nil {
		return err
	}
	l.created = append(l.created, file)
	l.written[file] = []byte(text)
	return nil
}

// append adds text to the log in one write and syncs it. The lock file
// records the log's size and digest after each append; a log that no
// longer matches them was changed some other way, and is refused.
func (l *lockedPlan) append(text string) error {
	return l.appendWith(func(current []byte, recorded string) (string, error) {
		if recorded != "" && recorded != digest(current) {
			return "", l.sealBroken()
		}
		return text, nil
	})
}

// sealBroken is append's refusal of a log changed some other way.
func (l *lockedPlan) sealBroken() error {
	return refusal.New("%s has changed since agent-plan last appended to it, and the log is append-only. Leave it as it is, and tell the operator which plan's log changed; `agent-plan reseal` is theirs to run", l.p.log())
}

// checkSeal refuses, as append would, while the log does not match the
// digest the lock file recorded, so a command whose event append would
// refuse writes no plan file first.
func (l *lockedPlan) checkSeal() error {
	recorded := make([]byte, 256)
	n, err := l.lock.ReadAt(recorded, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	seal := strings.TrimSpace(string(recorded[:n]))
	if seal == "" {
		return nil
	}
	current, err := os.ReadFile(l.p.log())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if seal != digest(current) {
		return l.sealBroken()
	}
	return nil
}

// appendWith appends what build returns, given the log and the digest
// recorded for it, as append does, and records the log's new digest.
func (l *lockedPlan) appendWith(build func(current []byte, recorded string) (string, error)) error {
	recorded := make([]byte, 256)
	n, err := l.lock.ReadAt(recorded, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	log, err := os.OpenFile(l.p.log(), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644) //nolint:gosec // a plan: notes on the work, synced through git
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
	// An event lands whole or not at all: part of one is a torn heading
	// that every later append refuses, and the plan change it records is
	// rolled back as if it had never been written.
	if n, err := log.Write([]byte(text)); err != nil || n != len(text) {
		err = fmt.Errorf("appending to %s: wrote %d of %d bytes: %v", l.p.log(), n, len(text), err)
		if n > 0 {
			if terr := log.Truncate(int64(len(current))); terr != nil {
				return fmt.Errorf("%w; removing the part written: %v", err, terr)
			}
		}
		return err
	}
	l.appended = true
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
// equal to the file is not written, and one that adds structure drift is
// refused before it is, as checkStructure says. The lock excludes other agent-plan
// processes but not Emacs, so edit refuses a file whose buffer has unsaved
// changes, and applies fn again to a file that changed between its read
// and the rename. The window between that last check and the rename stays
// open.
func (l *lockedPlan) edit(file string, fn func(lines []string) ([]string, error)) error {
	emacsLock := filepath.Join(filepath.Dir(file), ".#"+filepath.Base(file))
	if info, err := os.Lstat(emacsLock); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return refusal.New("%s has unsaved changes in an Emacs buffer (%s exists), which would overwrite this change when saved. Ask the operator to save or revert that buffer, then run the command again", file, emacsLock)
	}
	if err := l.checkSeal(); err != nil {
		return err
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
		if err := checkStructure(file, string(before), text); err != nil {
			return err
		}
		events, err := l.p.Events()
		if err != nil {
			return err
		}
		if err := checkKeywords(file, string(before), text, events); err != nil {
			return err
		}
		err = replaceFile(file, text, func() bool {
			now, err := os.ReadFile(file)
			return err == nil && bytes.Equal(now, before)
		})
		if err == nil {
			if _, ok := l.originals[file]; !ok {
				l.originals[file] = before
			}
			l.written[file] = []byte(text)
		}
		if !errors.Is(err, errChanged) {
			return err
		}
	}
	return refusal.New("%s kept changing while agent-plan edited it. Ask the operator what else is writing it, then run the command again", file)
}

// errChanged is replaceFile's error for a file that changed before the
// rename.
var errChanged = errors.New("changed before the rename")

// staleEdit is how old a temporary file of replaceFile's or writeNew's is
// before the next replaceFile clears it: older than any write takes, so it
// is one a killed process left. Every plan in the directory shares the
// prefix, each under its own lock, so a younger one may be another plan's
// write under way.
const staleEdit = 10 * time.Minute

// replaceFile writes text to path by renaming a new file over it, synced
// first, if unchanged, asked just before the rename, reports path as it
// was read. It clears the stale temporary files others left.
func replaceFile(path, text string, unchanged func() bool) error {
	if old, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".edit-*")); err == nil {
		for _, o := range old {
			if info, err := os.Lstat(o); err == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) > staleEdit {
				os.Remove(o)
			}
		}
	}
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
	if err := f.Sync(); err != nil {
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
	data, err := readPlan(p.log())
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
			Evidence: h.Props["EVIDENCE"], Review: h.Props["REVIEW"], Outcome: h.Props["OUTCOME"],
			Session: h.Props["SESSION"], Worktree: h.Props["WORKTREE"], Tip: h.Props["TIP"], Sign: h.Props["SIGN"],
			Repository: h.Props["REPOSITORY"], Revision: h.Props["REVISION"], OldRevision: h.Props["OLD_REVISION"],
			Exit: h.Props["EXIT"], Delivers: h.Props["DELIVERS"], Property: h.Props["PROPERTY"], Value: h.Props["VALUE"]}
		if _, summary, ok := strings.Cut(h.Title, " — "); ok {
			e.Summary = summary
		}
		e.Command, e.Dir, e.Base = h.Props["COMMAND"], h.Props["DIR"], h.Props["BASE"]
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
