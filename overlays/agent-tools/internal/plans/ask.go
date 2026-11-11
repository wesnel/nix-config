package plans

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// AskOptions are a question for the operator: the question, its priority, its
// context, the recommended answer and the others, and the IDs it waits
// for. With Answer, it is the agent's "reversible decision of your own"
// that plan-work describes, settled as it is written; Role is then the
// decided event's.
type AskOptions struct {
	// Part is the part whose file takes the question, or "" for the plan's.
	Part, Question, Priority, Context, Recommendation, Answer, Role string
	// Kind is one of QuestionKinds, or "" for a plain question.
	Kind                string
	Alternatives, After []string
}

// Asked is what Ask wrote.
type Asked struct {
	ID    string `json:"id"`
	File  string `json:"file"`
	Event Event  `json:"event"`
}

// priorities are the priority cookies Org reads by default.
var priorities = []string{"A", "B", "C"}

// Ask writes a question at the end of Questions in the plan's file, or the
// part's, under the next question ID of that file, in the form Questions
// reads, and logs an asked event. With an answer, it settles it at once,
// as Decide would, and logs a decided event too.
func (p Plan) Ask(o AskOptions, now time.Time) (Asked, error) {
	if err := p.checkOwn(); err != nil {
		return Asked{}, err
	}
	o.Question, o.Recommendation, o.Context = strings.TrimSpace(o.Question), strings.TrimSpace(o.Recommendation), strings.TrimSpace(o.Context)
	o.Answer = strings.TrimSpace(o.Answer)
	context, err := o.validate()
	if err != nil {
		return Asked{}, err
	}
	file := p.file(o.Part)
	if _, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) {
		return Asked{}, refusal.New("%s has no plan file for part %s. The part's sub-agent creates it with `agent-plan new --parent %s --part %s`", p.Name, o.Part, p.file(""), o.Part)
	}
	var asked Asked
	err = p.withLock(func(l *lockedPlan) error {
		list, err := p.itemList()
		if err != nil {
			return err
		}
		known := map[string]bool{}
		for _, it := range list {
			known[it.ID] = true
		}
		for _, id := range o.After {
			if !known[id] {
				return refusal.New("%q is not an item of %s, so the question would wait for it forever. Pass IDs `agent-plan status %s` lists", id, p.Name, p.file(""))
			}
		}
		qid := nextListedID(o.Part, "Q", known)
		block := o.block(qid, context)
		err = l.edit(file, func(lines []string) ([]string, error) {
			return o.insert(lines, block, file, qid, now)
		})
		if err != nil {
			return err
		}
		e, err := l.log(Event{Kind: "asked", Item: qid, Summary: o.Question, Role: o.Role}, now)
		asked = Asked{ID: qid, File: file, Event: e}
		if err != nil || o.Answer == "" {
			return err
		}
		_, err = l.log(Event{Kind: "decided", Item: qid, Summary: o.Answer, Role: o.Role}, now)
		return err
	})
	return asked, err
}

func (o AskOptions) validate() ([]string, error) {
	if o.Question == "" || o.Recommendation == "" {
		return nil, refusal.New("a question needs its text and your recommendation. Pass both, as `--question 'one line' --recommendation 'one line'`")
	}
	if o.Priority != "" && !oneOf(o.Priority, priorities) {
		return nil, refusal.New("%q is not a priority. Pass `--priority A`, `B` or `C`", o.Priority)
	}
	if o.Kind != "" && !oneOf(o.Kind, QuestionKinds) {
		return nil, refusal.New("%q is not a question's kind. Pass `--kind %s`, or no --kind for a plain question", o.Kind,
			strings.Join(QuestionKinds, "` or `--kind "))
	}
	if err := checkTitle("question", o.Question); err != nil {
		return nil, err
	}
	for what, v := range map[string]string{"recommendation": o.Recommendation, "answer": o.Answer} {
		if err := checkLine(what, v); err != nil {
			return nil, err
		}
	}
	if err := o.checkAlternatives(); err != nil {
		return nil, err
	}
	if o.Context != "" && strings.ContainsAny(o.Context[:1], "*#-+|:") {
		return nil, refusal.New("context %q starts with a character Org reads as structure. Pass it starting with a word, as `--context 'Because ...'`", o.Context)
	}
	context, err := textLines(o.Context)
	if err != nil {
		return nil, err
	}
	// Questions reads a question's list items as its alternatives.
	for _, l := range context {
		if listItem.MatchString(l) {
			return nil, refusal.New("context line %q is a list item, which a question's reader takes as an alternative. Pass alternatives with `--alternative`, and start the line with a word", l)
		}
	}
	if err := checkWords("after", o.After); err != nil {
		return nil, err
	}
	if o.Part != "" && !partPattern.MatchString(o.Part) {
		return nil, refusal.New("%q is not a part ID. Pass one such as `--part P1`", o.Part)
	}
	return context, nil
}

func (o AskOptions) block(qid string, context []string) []string {
	line := "*** " + kwAsk + " "
	if o.Priority != "" {
		line += "[#" + o.Priority + "] "
	}
	block := []string{line + o.Question, ":PROPERTIES:", ":CUSTOM_ID: " + qid, ":RECOMMENDATION: " + o.Recommendation}
	if o.Kind != "" {
		block = append(block, ":KIND: "+o.Kind)
	}
	if len(o.After) > 0 {
		block = append(block, ":AFTER: "+strings.Join(o.After, " "))
	}
	block = append(append(block, ":END:"), context...)
	for _, a := range o.Alternatives {
		block = append(block, "- "+strings.TrimSpace(a))
	}
	return block
}

func nextListedID[V any](part, letter string, items map[string]V) string {
	prefix := ""
	if part != "" {
		prefix = part + "."
	}
	next := 1
	pattern := idOf(prefix, letter)
	for id := range items {
		m := pattern.FindStringSubmatch(id)
		if m == nil {
			continue
		}
		if n, _ := strconv.Atoi(m[1]); n >= next {
			next = n + 1
		}
	}
	return fmt.Sprintf("%s%s%d", prefix, letter, next)
}

func (o AskOptions) insert(lines, block []string, file, qid string, now time.Time) ([]string, error) {
	_, end, ok := section(lines, "Questions")
	if !ok {
		return nil, refusal.New("%s has no `** Questions` heading. Ask the operator to restore it as the plan-work skill describes, then ask again", file)
	}
	// A section ending the file ends at its final empty line.
	if end == len(lines) && lines[end-1] == "" {
		end--
	}
	lines = append(append(append([]string{}, lines[:end]...), block...), lines[end:]...)
	if o.Answer == "" {
		return lines, nil
	}
	return settle(lines, qid, kwDecided, o.Answer, "agent", "", now)
}

func (o AskOptions) checkAlternatives() error {
	for _, a := range o.Alternatives {
		if err := checkLine("alternative", a); err != nil {
			return err
		}
		if strings.TrimSpace(a) == "" {
			return refusal.New("an alternative is empty. Pass each other answer as its own `--alternative 'one line'`")
		}
	}
	return nil
}
