// Package runs keeps this host's ledger of run records: what started a
// model session, as labels from fixed sets, which the usage report joins to
// the session's transcript by its ID.
//
// A launcher tags what it starts by setting Env to a Tag. Claude Code's
// SessionStart hook records a tagged session as it starts (FromSessionStart),
// and a launcher that reads a print session's stream-json output records the
// tokens its last result reports as it ends (Stream). A record holds no free
// text: no prompts, descriptions, titles or branch names, only the closed
// sets below, repository and plan names, pull request numbers, IDs, times,
// counts and the config directory.
package runs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/prreview"
)

// Env is the variable a launcher sets to its Tag's String, which the
// processes it starts inherit, Claude Code's hooks among them.
const Env = "AGENT_RUN_TAG"

// CategoryEnv is the variable the review queue sets for claude-pr-loop's
// triage session, which `plan-dash usage run --pr-loop` reads where it
// ignores an inherited Env: Triage, where the session would be a Review.
const CategoryEnv = "AGENT_RUN_CATEGORY"

// The categories a tag may name.
const (
	Loop        = "loop"
	Review      = "review"
	Triage      = "triage"
	FreshReview = "fresh-review"
	Dashboard   = "dashboard"
	Digest      = "digest"
	Diagram     = "diagram"
)

// Categories are every category a tag may name.
var Categories = []string{Loop, Review, Triage, FreshReview, Dashboard, Digest, Diagram}

// The kinds a tag may name: a review's, a fresh review's or a digest's.
const (
	Dispatched = "dispatched"
	Security   = "security"
	Compat     = "compat"
	Claims     = "claims"
	General    = "general"
	Standup    = "standup"
	Demo       = "demo"
)

// Kinds are every kind a tag may name.
var Kinds = []string{Dispatched, Security, Compat, Claims, General, Standup, Demo}

// Tag is what a launcher says of the runs it starts.
type Tag struct {
	Category string
	// Repo is OWNER/NAME.
	Repo string
	PR   int
	Kind string
	// Plan is a plan's file name.
	Plan string
}

// labelPart is what a plan must be.
var labelPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// validRepo is whether s is OWNER/NAME as pr-loop review keeps reviews
// under, so that any review's session can be tagged with its repository.
func validRepo(s string) bool {
	owner, name, ok := strings.Cut(s, "/")
	return ok && prreview.ValidName(owner) && prreview.ValidName(name)
}

// Validate reports the first of t's fields outside its set or shape.
func (t Tag) Validate() error {
	switch {
	case !slices.Contains(Categories, t.Category):
		return fmt.Errorf("runs: the category is one of %s, not %q", strings.Join(Categories, ", "), t.Category)
	case t.Repo != "" && !validRepo(t.Repo):
		return fmt.Errorf("runs: the repository is OWNER/NAME, not %q", t.Repo)
	case t.PR < 0:
		return fmt.Errorf("runs: the pull request is a positive number, not %d", t.PR)
	case t.Kind != "" && !slices.Contains(Kinds, t.Kind):
		return fmt.Errorf("runs: the kind is one of %s, not %q", strings.Join(Kinds, ", "), t.Kind)
	case t.Plan != "" && !labelPart.MatchString(t.Plan):
		return fmt.Errorf("runs: the plan is a plan's file name, not %q", t.Plan)
	}
	return nil
}

// String is t as Env holds it: KEY=VALUE pairs joined by semicolons,
// category first, empty fields left out.
func (t Tag) String() string {
	parts := []string{"category=" + t.Category}
	for _, kv := range [][2]string{{"repo", t.Repo}, {"kind", t.Kind}, {"plan", t.Plan}} {
		if kv[1] != "" {
			parts = append(parts, kv[0]+"="+kv[1])
		}
	}
	if t.PR > 0 {
		parts = append(parts, "pr="+strconv.Itoa(t.PR))
	}
	return strings.Join(parts, ";")
}

// ParseTag reads a tag as String writes it; spaces around a pair are
// ignored. A key that is unknown, repeated or empty, or a value outside
// its set or shape, refuses the whole tag.
func ParseTag(s string) (Tag, error) {
	var t Tag
	seen := map[string]bool{}
	for _, pair := range strings.Split(s, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || seen[key] || value == "" {
			return Tag{}, fmt.Errorf("runs: %q is not a tag of KEY=VALUE pairs, each key once with a value", s)
		}
		seen[key] = true
		switch key {
		case "category":
			t.Category = value
		case "repo":
			t.Repo = value
		case "pr":
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 || strconv.Itoa(n) != value {
				return Tag{}, fmt.Errorf("runs: the pull request is a positive number, not %q", value)
			}
			t.PR = n
		case "kind":
			t.Kind = value
		case "plan":
			t.Plan = value
		default:
			return Tag{}, fmt.Errorf("runs: a tag's keys are category, repo, pr, kind and plan, not %q", key)
		}
	}
	return t, t.Validate()
}

// Labels are a record's tag but its category.
type Labels struct {
	Repo string `json:"repo,omitempty"`
	PR   int    `json:"pr,omitempty"`
	Kind string `json:"kind,omitempty"`
	Plan string `json:"plan,omitempty"`
}

// Tokens are what a run reported using, by kind. A print session's
// result does not split its cache writes by lifetime.
type Tokens struct {
	Input      int64 `json:"input"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Output     int64 `json:"output"`
}

// Record is one line of the ledger. A run may have several, which Read
// merges by RunID: one as it starts, one as it ends.
type Record struct {
	V int `json:"v"`
	// RunID is "claude:" and the session ID for a Claude Code session.
	RunID     string    `json:"run_id"`
	Host      string    `json:"host,omitempty"`
	Started   time.Time `json:"started,omitzero"`
	Ended     time.Time `json:"ended,omitzero"`
	Provider  string    `json:"provider"`
	SessionID string    `json:"session_id,omitempty"`
	Category  string    `json:"category"`
	Labels    Labels    `json:"labels,omitzero"`
	// ConfigDir is the Claude Code config directory whose projects/ holds
	// the session's transcript.
	ConfigDir string `json:"config_dir,omitempty"`
	Model     string `json:"model,omitempty"`
	// Tokens are a print session's last result's: the process's totals,
	// its sub-agents' included.
	Tokens *Tokens `json:"tokens,omitempty"`
}

// Version is Record.V.
const Version = 1

var (
	sessionID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:_\[\]-]{0,99}$`)
	hostName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
)

func (r Record) tag() Tag {
	return Tag{Category: r.Category, Repo: r.Labels.Repo, PR: r.Labels.PR, Kind: r.Labels.Kind, Plan: r.Labels.Plan}
}

// Validate reports the first of r's fields outside its set or shape.
func (r Record) Validate() error {
	switch {
	case r.V != Version:
		return fmt.Errorf("runs: version %d is not %d", r.V, Version)
	case r.Provider != "claude" || !sessionID.MatchString(r.SessionID) || r.RunID != "claude:"+r.SessionID:
		return fmt.Errorf("runs: %q is not a Claude Code session's run", r.RunID)
	case r.Started.IsZero() && r.Ended.IsZero():
		return errors.New("runs: a record says when its run started or ended")
	case r.Host != "" && !hostName.MatchString(r.Host):
		return fmt.Errorf("runs: %q is not a host name", r.Host)
	case r.ConfigDir != "" && (!filepath.IsAbs(r.ConfigDir) || filepath.Clean(r.ConfigDir) != r.ConfigDir):
		return fmt.Errorf("runs: the config directory is a clean absolute path, not %q", r.ConfigDir)
	case r.Model != "" && !modelName.MatchString(r.Model):
		return fmt.Errorf("runs: %q is not a model's name", r.Model)
	}
	return r.tag().Validate()
}

func claudeRecord(session string, tag Tag, host, configDir string) Record {
	return Record{V: Version, RunID: "claude:" + session, Host: host, Provider: "claude", SessionID: session,
		Category: tag.Category, Labels: Labels{Repo: tag.Repo, PR: tag.PR, Kind: tag.Kind, Plan: tag.Plan},
		ConfigDir: configDir}
}

// FromSessionStart is the record of the session Claude Code's SessionStart
// hook input describes, tagged by tag, Env's value, as it starts. It
// reports false, with no error, for an untagged session, which the usage
// report attributes by its heuristics, and for a compaction or a resume,
// which keep the session ID a start or /clear recorded. Another event's
// input is an error: a hook listed under the wrong event would otherwise
// record a session's end as its start.
func FromSessionStart(input []byte, tag string, now time.Time, host, configDir string) (Record, bool, error) {
	var in struct {
		Event     string `json:"hook_event_name"`
		SessionID string `json:"session_id"`
		Source    string `json:"source"`
		Model     string `json:"model"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Record{}, false, fmt.Errorf("runs: the hook's input: %w", err)
	}
	if in.Event != "SessionStart" {
		return Record{}, false, fmt.Errorf("runs: the hook runs on SessionStart, not %q", in.Event)
	}
	if tag == "" || in.Source != "startup" && in.Source != "clear" {
		return Record{}, false, nil
	}
	t, err := ParseTag(tag)
	if err != nil {
		return Record{}, false, err
	}
	if !sessionID.MatchString(in.SessionID) {
		return Record{}, false, fmt.Errorf("runs: the hook's input names no session")
	}
	r := claudeRecord(in.SessionID, t, host, configDir)
	r.Started = now
	if modelName.MatchString(in.Model) {
		r.Model = in.Model
	}
	return r, true, r.Validate()
}

// maxLine bounds the stream line Stream reads, and the ledger line Read
// does: a longer one is skipped.
const maxLine = 4 << 20

// Stream reads a Claude Code print session's `--output-format
// stream-json` output, written to it as it arrives, for the last result's
// session and tokens. Each result carries the process's totals so far.
// Only results of the session the first init event names count: a
// command the session runs may print another's.
type Stream struct {
	line     []byte
	skipping bool
	init     string
	session  string
	model    string
	tokens   *Tokens
}

// Write takes the next of the output.
func (s *Stream) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		if !s.skipping {
			if len(s.line)+len(chunk) > maxLine {
				s.line, s.skipping = s.line[:0], true
			} else {
				s.line = append(s.line, chunk...)
			}
		}
		if i < 0 {
			break
		}
		if !s.skipping {
			s.result(s.line)
		}
		s.line, s.skipping = s.line[:0], false
		p = p[i+1:]
	}
	return n, nil
}

var (
	resultType  = []byte(`"type":"result"`)
	initSubtype = []byte(`"subtype":"init"`)
)

func (s *Stream) result(line []byte) {
	isResult, isInit := bytes.Contains(line, resultType), s.init == "" && bytes.Contains(line, initSubtype)
	if !isResult && !isInit {
		return
	}
	var in struct {
		Type       string `json:"type"`
		Subtype    string `json:"subtype"`
		SessionID  string `json:"session_id"`
		ModelUsage map[string]struct {
			Input      int64 `json:"inputTokens"`
			Output     int64 `json:"outputTokens"`
			CacheRead  int64 `json:"cacheReadInputTokens"`
			CacheWrite int64 `json:"cacheCreationInputTokens"`
		} `json:"modelUsage"`
	}
	if json.Unmarshal(line, &in) != nil || !sessionID.MatchString(in.SessionID) {
		return
	}
	if isInit && in.Type == "system" && in.Subtype == "init" {
		s.init = in.SessionID
		return
	}
	if in.Type != "result" || in.SessionID != s.init {
		return
	}
	var t Tokens
	model, most := "", int64(-1)
	for name, u := range in.ModelUsage {
		t.Input += u.Input
		t.Output += u.Output
		t.CacheRead += u.CacheRead
		t.CacheWrite += u.CacheWrite
		if total := u.Input + u.Output + u.CacheRead + u.CacheWrite; modelName.MatchString(name) &&
			(total > most || total == most && name < model) {
			model, most = name, total
		}
	}
	s.session, s.model, s.tokens = in.SessionID, model, &t
}

// Record is the run's record as it ends, tagged by tag, and false when no
// result was read: the process stopped before its first, and the report
// falls back on the transcript. A last line with no newline is read too.
func (s *Stream) Record(tag Tag, now time.Time, host, configDir string) (Record, bool) {
	if len(s.line) > 0 && !s.skipping {
		s.result(s.line)
		s.line = s.line[:0]
	}
	if s.tokens == nil {
		return Record{}, false
	}
	r := claudeRecord(s.session, tag, host, configDir)
	r.Ended, r.Model = now, s.model
	t := *s.tokens
	r.Tokens = &t
	return r, true
}

// Keep is how long a month's file is kept once the month has ended.
const Keep = 90 * 24 * time.Hour

// Ledger is the directory holding the ledger, one file per month,
// runs-YYYY-MM.jsonl, by the UTC time of each append.
type Ledger struct{ Dir string }

// Default is $XDG_STATE_HOME/agent-tools/usage, the state directory the
// build bakes in, which an agent's command cannot point elsewhere.
func Default() Ledger {
	return Ledger{Dir: filepath.Join(hostpaths.StateHome(), "agent-tools", "usage")}
}

var monthFile = regexp.MustCompile(`^runs-(\d{4}-\d{2})\.jsonl$`)

func monthEnd(name string) (time.Time, bool) {
	m := monthFile.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	start, err := time.Parse("2006-01", m[1])
	if err != nil {
		return time.Time{}, false
	}
	return start.AddDate(0, 1, 0), true
}

// directory refuses a ledger directory that is a symbolic link, through
// which a ledger would be written or read somewhere else.
func (l Ledger) directory() error {
	info, err := os.Lstat(l.Dir)
	if err == nil && !info.IsDir() {
		return fmt.Errorf("runs: the ledger %s is not a directory", l.Dir)
	}
	return err
}

// Append adds r to the month of now, as one write of one line, which
// appenders on this host do not interleave, never through a symbolic
// link; then it removes, as far as it can, the files of months that ended
// more than Keep before now, which fails no append.
func (l Ledger) Append(r Record, now time.Time) error {
	if err := r.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return err
	}
	if err := l.directory(); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(l.Dir, "runs-"+now.UTC().Format("2006-01")+".jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	entries, _ := os.ReadDir(l.Dir)
	for _, e := range entries {
		if end, ok := monthEnd(e.Name()); ok && now.Sub(end) > Keep {
			_ = os.Remove(filepath.Join(l.Dir, e.Name()))
		}
	}
	return nil
}

// Read is the runs that overlap the time from since on, by run ID, each
// run's records, from every month kept, merged: its earliest start, its
// latest end and that end's tokens, and the rest from its first record. A
// run with no end yet overlaps every time after its start. A line that is
// not a valid record, or is longer than maxLine, is skipped and counted.
// The ledger not existing is no error.
func (l Ledger) Read(since time.Time) (map[string]Record, int, error) {
	if err := l.directory(); os.IsNotExist(err) {
		return map[string]Record{}, 0, nil
	} else if err != nil {
		return nil, 0, err
	}
	entries, err := os.ReadDir(l.Dir)
	if err != nil {
		return nil, 0, err
	}
	var names []string
	for _, e := range entries {
		if _, ok := monthEnd(e.Name()); ok && e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	runs, latest := map[string]Record{}, map[string]time.Time{}
	unreadable := 0
	defer func() {
		for id, r := range runs {
			if !r.Ended.IsZero() && r.Ended.Before(since) {
				delete(runs, id)
			}
		}
	}()
	for _, name := range names {
		n, err := l.read(filepath.Join(l.Dir, name), func(r Record) {
			m, ok := runs[r.RunID]
			if !ok {
				m = r
				m.Ended, m.Tokens = time.Time{}, nil
			} else if !r.Started.IsZero() && (m.Started.IsZero() || r.Started.Before(m.Started)) {
				m.Started = r.Started
			}
			if !r.Ended.IsZero() && !r.Ended.Before(latest[r.RunID]) {
				latest[r.RunID] = r.Ended
				m.Ended, m.Tokens = r.Ended, r.Tokens
			}
			runs[r.RunID] = m
		})
		unreadable += n
		if err != nil {
			return runs, unreadable, err
		}
	}
	return runs, unreadable, nil
}

func (l Ledger) read(path string, each func(Record)) (int, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	unreadable := 0
	lines := bufio.NewReader(f)
	for {
		line, long, err := readLine(lines)
		if len(line) > 0 || long {
			var r Record
			if long || json.Unmarshal(line, &r) != nil || r.Validate() != nil {
				unreadable++
			} else {
				each(r)
			}
		}
		if err == io.EOF {
			return unreadable, nil
		} else if err != nil {
			return unreadable, err
		}
	}
}

// readLine is the next line without its newline, or reports it longer
// than maxLine having read past it.
func readLine(r *bufio.Reader) ([]byte, bool, error) {
	var line []byte
	long := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !long {
			if len(line)+len(chunk) > maxLine+1 {
				line, long = nil, true
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return bytes.TrimSuffix(line, []byte("\n")), long, err
	}
}
