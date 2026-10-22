// Package export writes, model-free, the documents the "Agent plans" page
// keeps in its claude.ai database: one per plan, every host's, and
// GitHub's status and the plans' sync state. Everything it writes leaves
// the host, so each document is a fixed struct whose JSON tags are the
// allowlist, each always written. Plan fields are read only from the plans
// directory, through plans.Config.Open, which refuses a file outside it or
// with another name: the title, the Goal section, the headings with IDs
// under Steps sections, the questions under Questions with their
// RECOMMENDATION, ANSWER or LOGBOOK note and list items, and STATUS; of
// the log, only event kinds and times. No other property, section, drawer
// or log text is read into a document. Free text is stripped of control
// and formatting characters, has known secret shapes redacted and is
// capped in runes, and lists are capped, so that a document stays within
// maxDoc. Redaction knows shapes, not data: what must not reach claude.ai
// must stay out of plans. A field added here is published; the sentinel
// test's allowlist names each.
package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/redact"
	"github.com/wesnel/agent-tools/internal/status"
)

// The caps on text, in runes.
const (
	short  = 200  // a title or a label
	medium = 500  // a question, or why an option
	answer = 1000 // a decision's answer
	long   = 2000 // a goal or a question's context
)

// The caps on a plan's lists. With the text caps, and at most four bytes
// a rune in JSON once text is stripped, they bound a plan's document at
// 134,200 runes of text, 536,800 bytes, plus its keys and IDs.
const (
	maxSteps     = 100
	maxDecisions = 30
	maxQuestions = 10
	maxOptions   = 6
)

// maxDoc is the most a document written may hold: under half of the
// 1 MiB an ArtifactData batch body takes, so a batch can carry any one
// document. Batching more is by size, not by count.
const maxDoc = 600 << 10

// maxID is the longest ArtifactData document ID.
const maxID = 200

// Plan is the document plans/<ID>: a plan that is not halted.
type Plan struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Goal  string `json:"goal"`
	// Status is STATUS, or "unknown" where it is not a lower-case word.
	Status string `json:"status"`
	// Host alone writes the plan.
	Host string `json:"host"`
	// Updated is, for this host's plan, the time of its log's newest event
	// and, for another host's, when its log last arrived in a commit:
	// RFC 3339 with the zone, or "".
	Updated   string     `json:"updated"`
	Steps     []Step     `json:"steps"`
	Decisions []Decision `json:"decisions"`
	// Questions are those the operator can answer now.
	Questions []Question `json:"questions"`
}

// Step is a heading with a step's ID under a Steps section of the plan or
// its parts, in file order.
type Step struct {
	ID    string `json:"id"`
	Order int    `json:"order"`
	Title string `json:"title"`
	// Keyword is the one the log gives it.
	Keyword string `json:"keyword"`
	Ready   bool   `json:"ready"`
}

// Decision is a question settled as DECIDED, the newest maxDecisions.
type Decision struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Question is an open question.
type Question struct {
	ID       string   `json:"id"`
	Text     string   `json:"text"`
	Priority string   `json:"priority"`
	Context  string   `json:"context"`
	Options  []Option `json:"options"`
}

// Option is one answer to a question. Key is the answer's place: A is
// always the recommendation and B on the alternatives, so a question
// without a recommendation starts at B, and a choice maps back to its
// answer by Key, never by its index in Options. An Org description item,
// "label :: why", gives Why.
type Option struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Why         string `json:"why"`
	Recommended bool   `json:"recommended"`
}

// Sync is the document meta/sync: when the export ran on Host, and how far
// Host's plans have reached the notes repository's remote, as
// plans.SyncState reads it; times are RFC 3339, or "".
type Sync struct {
	Updated  string `json:"updated"`
	Host     string `json:"host"`
	Repo     bool   `json:"repo"`
	Pushed   string `json:"pushed"`
	Unsynced bool   `json:"unsynced"`
	Fetched  string `json:"fetched"`
}

// GitHub is the document github/status: status.GitHubAt's result, At ""
// where no fetch ever succeeded.
type GitHub struct {
	At            string    `json:"at"`
	Stale         bool      `json:"stale"`
	Mine          []PR      `json:"mine"`
	MineTotal     int       `json:"mineTotal"`
	Requests      []Request `json:"requests"`
	RequestsTotal int       `json:"requestsTotal"`
}

// PR is status.PR.
type PR struct {
	Repo       string `json:"repo"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	Draft      bool   `json:"draft"`
	CI         string `json:"ci"`
	Review     string `json:"review"`
	Unresolved int    `json:"unresolved"`
}

// Request is status.Request, Requested RFC 3339 or "".
type Request struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Draft     bool   `json:"draft"`
	Team      bool   `json:"team"`
	Requested string `json:"requested"`
	Head      string `json:"head"`
	CI        string `json:"ci"`
	Record    string `json:"record"`
}

// Export is one export's documents. Failed are the IDs of plans found but
// not exported for an error, whose documents Write keeps; Listed is
// whether the plans directory was listed, without which Write removes no
// document.
type Export struct {
	Plans  []Plan
	Sync   Sync
	GitHub GitHub
	Failed []string
	Listed bool
}

// Collect reads c's plans and sync state, and GitHub through cache, as at
// now. Its error is what failed; the Export still holds what did not.
func Collect(ctx context.Context, c plans.Config, cache string, now time.Time) (Export, error) {
	var e Export
	var errs [3]error
	e.Plans, e.Failed, errs[0] = Plans(c, now)
	e.Listed = e.Plans != nil
	e.Sync, errs[1] = SyncOf(c, now)
	e.GitHub, errs[2] = GitHubOf(ctx, cache, now)
	return e, errors.Join(errs[:]...)
}

var (
	state    = regexp.MustCompile(`^[a-z][a-z-]*$`)
	priority = regexp.MustCompile(`^[A-Z0-9]$`)
	keywords = map[string]bool{"TODO": true, "STARTED": true, "DONE": true, "VERIFIED": true, "ABANDONED": true}
)

// Plans are c's plans of every host, newest first, but those halted, and
// the IDs of those that failed: refused by Open, unreadable, or with an ID
// longer than maxID, each named in the error. Plans is nil only where the
// plans directory could not be listed.
func Plans(c plans.Config, now time.Time) ([]Plan, []string, error) {
	all, failed, err := c.Plans()
	if err != nil {
		return nil, nil, fmt.Errorf("listing the plans: %w", err)
	}
	var errs []error
	for _, name := range failed {
		errs = append(errs, fmt.Errorf("%s: not a plan's own files, or its log is missing", name))
	}
	out := []Plan{}
	for _, p := range all {
		if len(p.Name) > maxID {
			failed = append(failed, p.Name)
			errs = append(errs, fmt.Errorf("%s: its ID is longer than the %d characters a document ID may be", p.Name, maxID))
			continue
		}
		plan, err := planOf(c, p, now)
		if err != nil {
			failed = append(failed, p.Name)
			errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
			continue
		}
		if plan.Status != "halted" {
			out = append(out, plan)
		}
	}
	return out, failed, errors.Join(errs...)
}

func planOf(c plans.Config, p plans.Plan, now time.Time) (Plan, error) {
	st, err := p.StatusAt(now)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{ID: p.Name, Title: text(st.Title, short), Status: st.State, Host: p.Host,
		Steps: []Step{}, Decisions: []Decision{}, Questions: []Question{}}
	if !state.MatchString(plan.Status) {
		plan.Status = "unknown"
	}
	if goal, err := p.Goal(""); err == nil {
		plan.Goal = text(strings.TrimSpace(goal), long)
	}
	if plan.Updated, err = updated(c, p); err != nil {
		return Plan{}, err
	}
	steps, err := p.InSection("Steps")
	if err != nil {
		return Plan{}, err
	}
	ready := map[string]bool{}
	for _, id := range st.ReadySteps {
		ready[id] = true
	}
	for _, s := range st.Steps {
		if !steps[s.ID] || len(plan.Steps) == maxSteps {
			continue
		}
		keyword := s.Keyword
		if !keywords[keyword] {
			keyword = "unknown"
		}
		plan.Steps = append(plan.Steps, Step{ID: s.ID, Order: len(plan.Steps), Title: text(s.Title, short),
			Keyword: keyword, Ready: ready[s.ID]})
	}
	ds, err := p.Decisions(true)
	if err != nil {
		return Plan{}, err
	}
	for _, d := range ds[max(0, len(ds)-maxDecisions):] {
		plan.Decisions = append(plan.Decisions, Decision{ID: d.ID, Question: text(d.Question, medium), Answer: text(d.Answer, answer)})
	}
	qs, err := p.Questions(true)
	if err != nil {
		return Plan{}, err
	}
	askable := plans.Askable(qs)
	for _, q := range askable[:min(len(askable), maxQuestions)] {
		question := Question{ID: q.ID, Text: text(q.Question, medium), Context: text(q.Context, long), Options: []Option{}}
		if priority.MatchString(q.Priority) {
			question.Priority = q.Priority
		}
		answers := append([]string{q.Recommendation}, q.Alternatives...)
		for i, a := range answers {
			if a == "" || len(question.Options) == maxOptions {
				continue
			}
			label, why, _ := strings.Cut(a, " :: ")
			question.Options = append(question.Options, Option{Key: string(rune('A' + i)), Label: text(label, short),
				Why: text(why, medium), Recommended: i == 0})
		}
		plan.Questions = append(plan.Questions, question)
	}
	return plan, nil
}

// updated is Plan.Updated of p: a log's times carry no zone, so only this
// host's are read as local time.
func updated(c plans.Config, p plans.Plan) (string, error) {
	if !p.Own {
		at, err := c.Committed(p)
		return stamp(at), err
	}
	events, err := p.Events()
	if err != nil {
		return "", err
	}
	var at time.Time
	for _, e := range events {
		if t, err := time.ParseInLocation("[2006-01-02 Mon 15:04]", e.Time, time.Local); err == nil && t.After(at) {
			at = t
		}
	}
	return stamp(at), nil
}

// SyncOf is c's sync state as at now.
func SyncOf(c plans.Config, now time.Time) (Sync, error) {
	s, err := c.SyncState()
	return Sync{Updated: stamp(now), Host: c.Host, Repo: s.Repo, Pushed: stamp(s.Pushed), Unsynced: s.Unsynced,
		Fetched: stamp(s.Fetched)}, err
}

var (
	slug     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	fullHead = regexp.MustCompile(`^[0-9a-f]{40}$`)
	word     = regexp.MustCompile(`^[a-z][a-z-]{0,30}$`)
)

// GitHubOf is status.GitHubAt's result through cache, as at now, keeping
// only the pull requests whose keys and heads have their shapes.
func GitHubOf(ctx context.Context, cache string, now time.Time) (GitHub, error) {
	g := GitHub{Mine: []PR{}, Requests: []Request{}}
	got, stale, err := status.GitHubAt(ctx, cache, now)
	if got == nil {
		return g, err
	}
	g.At, g.Stale, g.MineTotal, g.RequestsTotal = stamp(got.At), stale, got.MineTotal, got.RequestsTotal
	for _, p := range got.Mine {
		if slug.MatchString(p.Repo) && p.Number > 0 {
			g.Mine = append(g.Mine, PR{Repo: p.Repo, Number: p.Number, Title: text(p.Title, short), Draft: p.Draft,
				CI: enum(p.CI), Review: enum(p.Review), Unresolved: p.Unresolved})
		}
	}
	for _, r := range got.Requests {
		if slug.MatchString(r.Repo) && r.Number > 0 && fullHead.MatchString(r.Head) {
			g.Requests = append(g.Requests, Request{Repo: r.Repo, Number: r.Number, Title: text(r.Title, short),
				Draft: r.Draft, Team: r.Team, Requested: stamp(r.Requested), Head: r.Head, CI: enum(r.CI),
				Record: enum(r.Record)})
		}
	}
	return g, err
}

// enum is v where it is a lower-case word, as status writes its states,
// and "unknown" otherwise.
func enum(v string) string {
	if word.MatchString(v) {
		return v
	}
	return "unknown"
}

// text is s without control characters but tab and newline, nor format
// characters, such as bidirectional overrides and zero-width ones, which
// could reorder or hide what the page shows, nor line and paragraph
// separators; then with known secret shapes redacted, and cut to max
// runes. Stripping comes first, so a hidden character cannot split a
// secret's shape.
func text(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
	s = redact.Secrets(s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

// stamp is t in RFC 3339, or "" for the zero time.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// collections are the directories Write writes in.
var collections = []string{"plans", "github", "meta"}

// Write puts e's documents under dir, as an ArtifactData read's out_dir
// lays them out, <collection>/<doc_id>.json: plans/<ID>.json,
// github/status.json and meta/sync.json, each replaced by a rename. It
// refuses a collection directory that is a link. Where e.Listed, it removes
// the plan documents of plans neither exported nor failed; nothing else in
// dir is touched.
func Write(dir string, e Export) error {
	for _, collection := range collections {
		info, err := os.Lstat(filepath.Join(dir, collection))
		switch {
		case err == nil && !info.IsDir():
			return fmt.Errorf("%s is not a directory, which export writes in", filepath.Join(dir, collection))
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	keep := map[string]bool{}
	for _, id := range e.Failed {
		keep[id+".json"] = true
	}
	errs := []error{os.MkdirAll(filepath.Join(dir, "plans"), 0o700)}
	for _, p := range e.Plans {
		name := p.ID + ".json"
		keep[name] = true
		errs = append(errs, writeJSON(filepath.Join(dir, "plans", name), p))
	}
	errs = append(errs, writeJSON(filepath.Join(dir, "github", "status.json"), e.GitHub),
		writeJSON(filepath.Join(dir, "meta", "sync.json"), e.Sync))
	if !e.Listed {
		return errors.Join(errs...)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "plans"))
	errs = append(errs, err)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") && !keep[entry.Name()] {
			errs = append(errs, os.Remove(filepath.Join(dir, "plans", entry.Name())))
		}
	}
	return errors.Join(errs...)
}

// writeJSON writes v to file, unescaped for HTML, which the page never
// parses it as; or refuses it where it is over maxDoc.
func writeJSON(file string, v any) error {
	var b strings.Builder
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		return err
	}
	if b.Len() > maxDoc {
		return fmt.Errorf("%s would hold %d bytes, over the %d a document may", file, b.Len(), maxDoc)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(b.String())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), file)
}
