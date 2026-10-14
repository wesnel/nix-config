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

// AskOptions are a question for Wesley: the question, its context, the
// recommended answer and the others, and the IDs it waits for.
type AskOptions struct {
	// Part is the part whose file takes the question, or "" for the plan's.
	Part, Question, Context, Recommendation string
	Alternatives, After                     []string
}

// Asked is what Ask wrote.
type Asked struct {
	ID    string `json:"id"`
	File  string `json:"file"`
	Event Event  `json:"event"`
}

// Ask writes a question at the end of Open questions in the plan's file,
// or the part's, under the next question ID of that file, in the form
// Questions reads, and logs an asked event.
func (p Plan) Ask(o AskOptions, now time.Time) (Asked, error) {
	if err := p.checkOwn(); err != nil {
		return Asked{}, err
	}
	o.Question, o.Recommendation, o.Context = strings.TrimSpace(o.Question), strings.TrimSpace(o.Recommendation), strings.TrimSpace(o.Context)
	if o.Question == "" || o.Recommendation == "" {
		return Asked{}, refusal.New("a question needs its text and your recommendation. Pass both, as `--question 'one line' --recommendation 'one line'`")
	}
	for what, v := range map[string]string{"question": o.Question, "recommendation": o.Recommendation, "context": o.Context} {
		if err := checkLine(what, v); err != nil {
			return Asked{}, err
		}
	}
	for _, a := range o.Alternatives {
		if err := checkLine("alternative", a); err != nil {
			return Asked{}, err
		}
		if strings.TrimSpace(a) == "" {
			return Asked{}, refusal.New("an alternative is empty. Pass each other answer as its own `--alternative 'one line'`")
		}
	}
	if o.Context != "" && strings.ContainsAny(o.Context[:1], "*#-+|:") {
		return Asked{}, refusal.New("context %q starts with a character Org reads as structure. Reword it to start with a word", o.Context)
	}
	if err := checkWords("after", o.After); err != nil {
		return Asked{}, err
	}
	if o.Part != "" && !partPattern.MatchString(o.Part) {
		return Asked{}, refusal.New("%q is not a part ID. Pass one such as `--part P1`", o.Part)
	}
	file := p.file(o.Part)
	if _, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) {
		return Asked{}, refusal.New("%s has no plan file for part %s. The part's sub-agent creates it with `agent-plan new --parent %s --part %s`", p.Name, o.Part, p.file(""), o.Part)
	}
	var asked Asked
	err := p.withLock(func(l *lockedPlan) error {
		list, err := p.itemList()
		if err != nil {
			return err
		}
		known := map[string]bool{}
		for _, it := range list {
			known[it.ID] = true
			if q := it.Props["QUESTION"]; q != "" {
				known[q] = true
			}
		}
		for _, id := range o.After {
			if !known[id] {
				return refusal.New("%q is not an item of %s, so the question would wait for it forever. Pass IDs `agent-plan status %s` lists", id, p.Name, p.file(""))
			}
		}
		prefix := ""
		if o.Part != "" {
			prefix = o.Part + "."
		}
		next := 1
		question := idOf(prefix, "Q")
		for id := range known {
			if m := question.FindStringSubmatch(id); m != nil {
				if n, _ := strconv.Atoi(m[1]); n >= next {
					next = n + 1
				}
			}
		}
		qid := fmt.Sprintf("%sQ%d", prefix, next)
		block := []string{"*** " + o.Question, ":PROPERTIES:", ":ID: " + qid, ":RECOMMENDATION: " + o.Recommendation}
		if len(o.After) > 0 {
			block = append(block, ":AFTER: "+strings.Join(o.After, " "))
		}
		block = append(block, ":END:")
		if o.Context != "" {
			block = append(block, o.Context)
		}
		for _, a := range o.Alternatives {
			block = append(block, "- "+strings.TrimSpace(a))
		}
		err = l.edit(file, func(lines []string) ([]string, error) {
			_, end, ok := section(lines, "Open questions")
			if !ok {
				return nil, refusal.New("%s has no `** Open questions` heading. Restore it as the plan-work skill describes, then ask again", file)
			}
			// A section ending the file ends at its final empty line.
			if end == len(lines) && lines[end-1] == "" {
				end--
			}
			return append(append(append([]string{}, lines[:end]...), block...), lines[end:]...), nil
		})
		if err != nil {
			return err
		}
		e, err := l.log(Event{Kind: "asked", Item: qid, Summary: o.Question}, now)
		asked = Asked{ID: qid, File: file, Event: e}
		return err
	})
	return asked, err
}
