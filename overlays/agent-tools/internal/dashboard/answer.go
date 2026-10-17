// Package dashboard is the "Agent plans" page's session on the host: the
// answers it takes from the page into the plans, and the watch that wakes
// it when the plans change.
package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/wesnel/agent-tools/internal/agentplans"
	"github.com/wesnel/agent-tools/internal/export"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/queue"
)

// QuestionsFile is the questions the last export put on the page, outside
// Out, which the session can write.
func QuestionsFile(p queue.Paths) string { return filepath.Join(p.Dir, "questions.json") }

// listing is QuestionsFile's content: each exported plan's host, and the
// open questions of this host's plans, each option's label by its key.
type listing struct {
	Plans     map[string]string `json:"plans"`
	Questions []listed          `json:"questions"`
}

type listed struct {
	Plan    string            `json:"plan"`
	ID      string            `json:"id"`
	Options map[string]string `json:"options"`
}

// WriteQuestions records exported, the plans an export put on the page, as
// the ones Answer checks the page's answers against: only host's plans'
// questions can be answered here, since only their host writes a plan.
func WriteQuestions(p queue.Paths, host string, exported []export.Plan) error {
	l := listing{Plans: map[string]string{}, Questions: []listed{}}
	for _, plan := range exported {
		l.Plans[plan.ID] = plan.Host
		if plan.Host != host {
			continue
		}
		for _, q := range plan.Questions {
			options := map[string]string{}
			for _, o := range q.Options {
				options[o.Key] = o.Label
			}
			l.Questions = append(l.Questions, listed{Plan: plan.ID, ID: q.ID, Options: options})
		}
	}
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return writeFile(QuestionsFile(p), data)
}

func readListing(p queue.Paths) (listing, error) {
	var l listing
	data, err := os.ReadFile(QuestionsFile(p))
	if errors.Is(err, fs.ErrNotExist) {
		return l, fmt.Errorf("no export has listed the plans' questions yet: run `plan-dash export` first")
	}
	if err != nil {
		return l, err
	}
	if err := json.Unmarshal(data, &l); err != nil {
		return l, fmt.Errorf("%s: %w", QuestionsFile(p), err)
	}
	return l, nil
}

// AnswerDoc is the answers/<PLAN>~<QID> document Answer writes back for the
// page: the choice it took, the state the answer reached, when, and why a
// refused one was.
type AnswerDoc struct {
	Schema int    `json:"schema"`
	Choice string `json:"choice"`
	State  string `json:"state"`
	At     string `json:"at"`
	Reason string `json:"reason"`
}

// The states of an answer: recorded as the operator's decision; settled,
// the question no longer open; refused.
const (
	Recorded = "recorded"
	Settled  = "settled"
	Refused  = "refused"
)

// pageAnswer is what Answer reads of a page's document: Choice, an option's
// key or "other" with Text, one line. A document with State is one Answer
// wrote, not the page's request.
type pageAnswer struct {
	State  string `json:"state"`
	Choice string `json:"choice"`
	Text   string `json:"text"`
}

// AnswerResult is what one Answer did, by document ID alone.
type AnswerResult struct {
	Recorded []string `json:"recorded"`
	Settled  []string `json:"settled"`
	Refused  []string `json:"refused"`
	// Ignored counts the entries of the answers directory that named no
	// document; those that were files are removed.
	Ignored int `json:"ignored"`
}

var (
	// answerID is an answers document's ID, PLAN~QID.
	answerID = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2}-[a-z0-9]+(?:-[a-z0-9]+)*\.[A-Za-z0-9][A-Za-z0-9_-]*)~((?:P[0-9]+\.)?Q[0-9]+)$`)
	// key is an option's key, as the export gives it.
	key = regexp.MustCompile(`^[A-Z]$`)
)

// maxAnswerDoc is the most of a page's answers document read; the page
// writes a few short fields.
const maxAnswerDoc = 16 << 10

// maxOther is the longest answer of the operator's own, in runes.
const maxOther = 1000

// Other is the choice of an answer of the operator's own.
const Other = "other"

// Refusals, as the page shows them.
const (
	notListed  = "not an open question the page lists"
	elsewhere  = "a plan of another host, which only that host writes: answer it there"
	noOption   = "not an option the page lists for this question"
	badOther   = "an answer of your own is one line of at most 1000 characters, without control or formatting characters"
	unreadable = "not an answers document: too large, not JSON, or not a file"
	ambiguous  = "two files name this document"
	notTaken   = "the plan did not take the answer"
)

// Answer takes the page's answers from Out/answers into the plans: only an
// answer to an open question of this host's plan the last export listed,
// by its option's key or one line of the operator's own, recorded as the
// phone's decision. It writes each document's state back there, and
// removes files that name no document. Nothing of a document but its
// state, choice and text is read, and the text reaches a plan only as one
// line the operator wrote.
func Answer(p queue.Paths, c plans.Config, now time.Time) (AnswerResult, error) {
	res := AnswerResult{Recorded: []string{}, Settled: []string{}, Refused: []string{}}
	l, err := readListing(p)
	if err != nil {
		return res, err
	}
	dir, err := queue.OpenOut(p, "answers")
	if err != nil {
		return res, err
	}
	defer dir.Close()
	files, err := queue.ReadNames(dir)
	if err != nil {
		return res, err
	}
	named := map[string][]string{}
	var ids []string
	for _, f := range files {
		id, ok := strings.CutSuffix(f.Name(), ".json")
		id = unspell(id)
		switch {
		case f.IsDir():
			res.Ignored++
			continue
		case !ok || len(id) > 200 || !answerID.MatchString(id):
			dir.Remove(f.Name())
			res.Ignored++
			continue
		}
		if named[id] == nil {
			ids = append(ids, id)
		}
		named[id] = append(named[id], f.Name())
	}
	slices.Sort(ids)
	var errs []error
	for _, id := range ids {
		var doc pageAnswer
		ok := len(named[id]) == 1 && queue.ReadObject(dir, named[id][0], maxAnswerDoc, &doc)
		for _, name := range named[id] {
			if name != docFile(id) {
				dir.Remove(name)
			}
		}
		if ok && doc.State != "" {
			continue
		}
		out := AnswerDoc{Schema: agentplans.Schema, At: now.UTC().Format(time.RFC3339)}
		if key.MatchString(doc.Choice) || doc.Choice == Other {
			out.Choice = doc.Choice
		}
		switch {
		case len(named[id]) > 1:
			out.State, out.Reason = Refused, ambiguous
		case !ok:
			out.State, out.Reason = Refused, unreadable
		default:
			out.State, out.Reason = decide(c, l, id, doc, now)
		}
		switch out.State {
		case Recorded:
			res.Recorded = append(res.Recorded, id)
		case Settled:
			res.Settled = append(res.Settled, id)
		default:
			res.Refused = append(res.Refused, id)
		}
		errs = append(errs, queue.WriteObject(dir, docFile(id), out))
	}
	return res, errors.Join(errs...)
}

// decide records doc, the page's answer document id, as the phone's
// decision where l lists its question and option, and says what came of
// it.
func decide(c plans.Config, l listing, id string, doc pageAnswer, now time.Time) (state, reason string) {
	m := answerID.FindStringSubmatch(id)
	plan, qid := m[1], m[2]
	host, ok := l.Plans[plan]
	switch {
	case !ok:
		return Refused, notListed
	case host != c.Host:
		return Refused, elsewhere
	}
	i := slices.IndexFunc(l.Questions, func(q listed) bool { return q.Plan == plan && q.ID == qid })
	if i < 0 {
		return Refused, notListed
	}
	var answer string
	switch {
	case doc.Choice == Other:
		answer = strings.TrimSpace(doc.Text)
		if !oneLine(answer) {
			return Refused, badOther
		}
		answer = orgInert(answer)
	case key.MatchString(doc.Choice) && l.Questions[i].Options[doc.Choice] != "":
		answer = l.Questions[i].Options[doc.Choice]
	default:
		return Refused, noOption
	}
	p, err := c.Open(filepath.Join(c.Dir, plan+".org"))
	if err != nil || !p.Own {
		return Refused, notListed
	}
	open, err := p.Questions(true)
	if err != nil {
		return Refused, notTaken
	}
	if !slices.ContainsFunc(open, func(q plans.Question) bool { return q.ID == qid }) {
		return Settled, ""
	}
	if _, err := p.Decide(qid, plans.DecideOptions{Answer: answer, Role: "phone"}, now); err != nil {
		return Refused, notTaken
	}
	return Recorded, ""
}

// oneLine reports whether s is a non-empty line of at most maxOther runes
// without control, formatting or separator characters.
func oneLine(s string) bool {
	if s == "" || len([]rune(s)) > maxOther {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool {
		return r == unicode.ReplacementChar || unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp)
	})
}

// trailingTags is what Org reads as a heading's tags at the end of a line.
var trailingTags = regexp.MustCompile(`\s(:[\pL\pN_@#%]+)+:$`)

// orgInert is s, an answer that lands in the log's Org heading and an Org
// property, unable to form a link or the heading's tags: "[[" and "]]" are
// split, and a line ending in tags ends in a period after them.
func orgInert(s string) string {
	s = strings.NewReplacer("[[", "[ [", "]]", "] ]").Replace(s)
	if trailingTags.MatchString(s) {
		s += "."
	}
	return s
}

// docFile is document id's file as an ArtifactData read with out_dir names
// it: "~" spelled "@".
func docFile(id string) string { return strings.ReplaceAll(id, "~", "@") + ".json" }

// unspell is the document ID a file's name without ".json" spells: "@" as
// the one separator of PLAN@QID, as a read writes it, or name as it is.
// Neither a plan's ID nor a question's holds "@" or "~".
func unspell(name string) string {
	if strings.Count(name, "@") == 1 && !strings.Contains(name, "~") {
		return strings.ReplaceAll(name, "@", "~")
	}
	return name
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
