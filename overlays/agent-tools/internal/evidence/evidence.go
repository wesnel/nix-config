// Package evidence keeps this host's log of the shell calls agents make:
// for each, the command, its exit status where the harness reports one,
// the worktree's HEAD and tree, and the last lines of its output, with
// known secret shapes taken out before anything is written. The PostToolUse
// hook (`pr-loop guard --post`) writes it and `git fresh-review audit`
// reads it, so that a verification claim is matched against what ran
// rather than what an agent says ran.
//
// The log is under agent-tools' XDG state directory, as hostpaths has it,
// which the guard refuses agents' writes to. It stays on this host: nothing
// syncs that directory, and it must not join the plans, which are synced.
package evidence

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/redact"
)

const (
	// Keep is how long a record is kept: a day's log is removed, when a
	// log is next written or read, once that day ended longer ago, and an
	// older record is not read.
	Keep = 14 * 24 * time.Hour
	// TailLines is how many of a call's last output lines a record holds.
	TailLines = 40
	// maxLine bounds each of those lines, in bytes.
	maxLine = 1000
	// maxCommandLine and maxCommand bound a record's command: each line,
	// and all of it from its start.
	maxCommandLine = 4 << 10
	maxCommand     = 16 << 10
	// preLine bounds each line before redaction, which takes time in
	// proportion to the text; it is far above what a record keeps, so no
	// secret that a record keeps a part of is cut by it.
	preLine = 64 << 10
	// maxRecord bounds a record as written, JSON escapes and all.
	maxRecord = 64 << 10
	// cutMark marks where text was cut.
	cutMark = "…"
	// maxUntracked is the size above which an untracked file is left out
	// of the tree, so that hashing it does not hold up every call.
	maxUntracked = 1 << 20
)

// Record is one shell call.
type Record struct {
	Time    time.Time `json:"time"`
	Session string    `json:"session"`
	// AgentID names the sub-agent that made the call, where the harness
	// names one.
	AgentID string `json:"agentId,omitempty"`
	// Cwd is where the call ran: the hook's working directory as a leading
	// cd or git -C in the command moves it.
	Cwd string `json:"cwd"`
	// Head is the commit checked out where the call ran, and Tree the tree
	// of the worktree's files as they were after it, untracked ones too:
	// HEAD's own tree where nothing was changed. Both are empty where the
	// command leaves unclear where it ran, and Cwd is then the hook's.
	Head string `json:"head,omitempty"`
	Tree string `json:"tree,omitempty"`
	// Skipped are the untracked files larger than maxUntracked that Tree
	// leaves out.
	Skipped []string `json:"skipped,omitempty"`
	Command string   `json:"command"`
	// Exit is the call's exit status, nil where the harness does not say:
	// Codex's hook never does, and a backgrounded command's end reaches
	// no hook.
	Exit *int `json:"exit"`
	// Background is a command the harness left running.
	Background bool   `json:"background,omitempty"`
	OutputTail string `json:"outputTail"`
}

// Dir is where the logs are, one file per session and day.
func Dir() string { return filepath.Join(hostpaths.StateHome(), "agent-tools", "evidence") }

// sessionName is a session ID that can name a file of its own.
var sessionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)

// dayFile is the part of a log's name after its session and a dot.
var dayFile = regexp.MustCompile(`^([0-9]{8})\.jsonl$`)

// dayLayout is a log's day, in UTC, as its name gives it.
const dayLayout = "20060102"

// file is the log of session's records made on day.
func file(session string, day time.Time) (string, error) {
	if !sessionName.MatchString(session) {
		return "", errors.New("evidence: a session ID of letters, digits, '.', '_' and '-' names its log")
	}
	return filepath.Join(Dir(), session+"."+day.UTC().Format(dayLayout)+".jsonl"), nil
}

// Append writes r to its session's log of the day, readable by the user
// alone: redacted, its command cut to maxCommand and its output to
// TailLines, so that the record stays within maxRecord. It removes the logs
// whose time is past (prune).
func Append(r Record) error {
	path, err := file(r.Session, r.Time)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	prune(r.Time)
	r.Command = clip(redact.Secrets(clip(r.Command, preLine, 4*preLine)), maxCommandLine, maxCommand)
	r.OutputTail = Tail(redact.Secrets(tail(r.OutputTail, preLine)))
	line, err := json.Marshal(r)
	for err == nil && len(line) > maxRecord {
		switch {
		case len(r.OutputTail) > 64:
			r.OutputTail = cutMark + keepEnd(r.OutputTail, len(r.OutputTail)/2)
		case len(r.Command) > 64:
			r.Command = cutLine(r.Command, len(r.Command)/2)
		default:
			return errors.New("evidence: a record too large to write")
		}
		line, err = json.Marshal(r)
	}
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	// One write per record, so that calls ending at once do not interleave.
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// prune removes each day's log whose day ended longer than Keep before
// now, and any other log untouched for as long.
func prune(now time.Time) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		stale := false
		dot := strings.LastIndexByte(strings.TrimSuffix(name, ".jsonl"), '.')
		if m := dayFile.FindStringSubmatch(name[dot+1:]); dot >= 0 && m != nil {
			day, err := time.Parse(dayLayout, m[1])
			stale = err == nil && now.Sub(day.Add(24*time.Hour)) > Keep
		} else if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			stale = now.Sub(info.ModTime()) > Keep
		}
		if stale {
			os.Remove(filepath.Join(Dir(), name))
		}
	}
}

// Tail is the last TailLines lines of text, each cut to maxLine bytes.
func Tail(text string) string { return tail(text, maxLine) }

// tail is the last TailLines lines of text, each cut to max bytes.
func tail(text string, max int) string {
	trimmed := strings.TrimSuffix(text, "\n")
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > TailLines {
		lines = lines[len(lines)-TailLines:]
	}
	for i, l := range lines {
		lines[i] = cutLine(l, max)
	}
	return strings.Join(lines, "\n") + "\n"
}

// clip is text from its start, each line cut to line bytes and the whole
// to about total.
func clip(text string, line, total int) string {
	lines := strings.Split(text, "\n")
	n := 0
	for i, l := range lines {
		l = cutLine(l, line)
		if n+len(l) > total {
			return strings.Join(append(lines[:i], cutMark), "\n")
		}
		lines[i] = l
		n += len(l) + 1
	}
	return strings.Join(lines, "\n")
}

// cutLine is s cut to at most n bytes, at a character's start, and marked.
func cutLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + cutMark
}

// keepEnd is the last n bytes or so of s, from a character's start.
func keepEnd(s string, n int) string {
	at := len(s) - n
	for at < len(s) && !utf8.RuneStart(s[at]) {
		at++
	}
	return s[at:]
}

// Read is the records of sessions no older than Keep before now, oldest
// first within each session, sessions in the order given. A session with
// no log has none. It removes the logs whose time is past (prune).
func Read(sessions []string, now time.Time) ([]Record, error) {
	prune(now)
	entries, err := os.ReadDir(Dir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var out []Record
	for _, s := range sessions {
		if _, err := file(s, now); err != nil {
			return nil, err
		}
		// ReadDir sorts by name, and so the session's days in order.
		for _, e := range entries {
			name, ok := strings.CutPrefix(e.Name(), s+".")
			if !ok || !dayFile.MatchString(name) {
				continue
			}
			f, err := os.Open(filepath.Join(Dir(), e.Name()))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			records, err := decode(f, now)
			f.Close()
			if err != nil {
				return nil, err
			}
			out = append(out, records...)
		}
	}
	return out, nil
}

// decode reads a log's records no older than Keep before now, skipping a
// line that is not one, such as a write cut short, however long it is.
func decode(r io.Reader, now time.Time) ([]Record, error) {
	var out []Record
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		var rec Record
		if len(line) > 0 && json.Unmarshal(line, &rec) == nil && now.Sub(rec.Time) <= Keep {
			out = append(out, rec)
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// Worktree is the HEAD of the worktree containing dir, and the tree of its
// files as they are, untracked ones too and ignored ones not; "" for each
// it cannot tell. The tree leaves out, and skipped names, the untracked
// files larger than maxUntracked. It is written through a copy of the index
// into a temporary object directory that borrows the repository's objects,
// so the index and the repository stay as they were, and only where the
// repository's config names no program git would run on adding the files
// (git.CheckConfig).
func Worktree(dir string) (head, tree string, skipped []string) {
	top, err := git.Run("-C", dir, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return "", "", nil
	}
	head, _ = git.Run("-C", top, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	if git.CheckConfig(top) != nil {
		return head, "", nil
	}
	paths, err := git.Run("-C", top, "rev-parse", "--path-format=absolute", "--git-path", "index", "--git-path", "objects")
	index, objects, ok := strings.Cut(paths, "\n")
	// The alternates variable separates directories with colons.
	if err != nil || !ok || strings.ContainsAny(objects, ":\"\n") {
		return head, "", nil
	}
	tmp, err := os.MkdirTemp("", "evidence-")
	if err != nil {
		return head, "", nil
	}
	defer os.RemoveAll(tmp)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index"), "GIT_OBJECT_DIRECTORY=" + filepath.Join(tmp, "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + objects}
	if os.Mkdir(filepath.Join(tmp, "objects"), 0o700) != nil {
		return head, "", nil
	}
	// A copy keeps the index's file stats, so that only changed files are
	// read again.
	if data, err := os.ReadFile(index); err == nil {
		if os.WriteFile(filepath.Join(tmp, "index"), data, 0o600) != nil { //nolint:gosec // a file in the temporary directory just made
			return head, "", nil
		}
	} else if head != "" {
		if _, err := git.RunEnv(env, "-C", top, "read-tree", head); err != nil {
			return head, "", nil
		}
	}
	untracked, err := git.RunEnv(env, "-C", top, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return head, "", nil
	}
	args := []string{"-C", top, "add", "-A", "--", "."}
	for _, p := range strings.Split(untracked, "\x00") {
		if info, err := os.Lstat(filepath.Join(top, p)); p != "" && err == nil && info.Mode().IsRegular() && info.Size() > maxUntracked {
			skipped = append(skipped, p)
			args = append(args, ":(exclude,literal)"+p)
		}
	}
	// Past this many, the arguments grow long; the tree goes unrecorded.
	if len(skipped) > 100 {
		return head, "", skipped[:100]
	}
	if _, err := git.RunEnv(env, args...); err != nil {
		return head, "", skipped
	}
	tree, err = git.RunEnv(env, "-C", top, "write-tree")
	if err != nil {
		return head, "", skipped
	}
	return head, tree, skipped
}
