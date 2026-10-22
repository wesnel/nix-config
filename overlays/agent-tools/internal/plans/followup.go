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

// FollowupOptions are work found while doing an item, outside its scope:
// its title, the item it was found in, and its context. Part is the part
// whose file takes it, or "" for the plan's.
type FollowupOptions struct {
	Title, FoundIn, Context, Part string
}

// Recorded is what Followup wrote.
type Recorded struct {
	ID    string `json:"id"`
	File  string `json:"file"`
	Event Event  `json:"event"`
}

// Followup writes a follow-up at the end of Follow-ups in the plan's file,
// or the part's, as a TODO under the next follow-up ID of that file, and
// logs a discovered event. Its keyword then follows its log, as a step's
// does.
func (p Plan) Followup(o FollowupOptions, now time.Time) (Recorded, error) {
	if err := p.checkOwn(); err != nil {
		return Recorded{}, err
	}
	o.Title, o.Context = strings.TrimSpace(o.Title), strings.TrimSpace(o.Context)
	if o.Title == "" || o.FoundIn == "" {
		return Recorded{}, refusal.New("a follow-up needs its title and the item it was found in. Pass both, as `--title 'one line' --found-in S1`")
	}
	if err := checkTitle("title", o.Title); err != nil {
		return Recorded{}, err
	}
	if err := checkLine("found-in", o.FoundIn); err != nil {
		return Recorded{}, err
	}
	if o.Context != "" && strings.ContainsAny(o.Context[:1], "*#-+|:") {
		return Recorded{}, refusal.New("context %q starts with a character Org reads as structure. Pass it starting with a word, as `--context 'Found while ...'`", o.Context)
	}
	context, err := textLines(o.Context)
	if err != nil {
		return Recorded{}, err
	}
	if o.Part != "" && !partPattern.MatchString(o.Part) {
		return Recorded{}, refusal.New("%q is not a part ID. Pass one such as `--part P1`", o.Part)
	}
	file := p.file(o.Part)
	if _, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) {
		return Recorded{}, refusal.New("%s has no plan file for part %s. The part's sub-agent creates it with `agent-plan new --parent %s --part %s`", p.Name, o.Part, p.file(""), o.Part)
	}
	var r Recorded
	err = p.withLock(func(l *lockedPlan) error {
		items, err := p.items()
		if err != nil {
			return err
		}
		if _, ok := items[o.FoundIn]; !ok {
			return refusal.New("%q is not an item of %s. Pass the ID of the item the work was found in, as `agent-plan status %s` lists it", o.FoundIn, p.Name, p.file(""))
		}
		prefix := ""
		if o.Part != "" {
			prefix = o.Part + "."
		}
		next := 1
		followup := idOf(prefix, "F")
		for id := range items {
			if m := followup.FindStringSubmatch(id); m != nil {
				if n, _ := strconv.Atoi(m[1]); n >= next {
					next = n + 1
				}
			}
		}
		id := fmt.Sprintf("%sF%d", prefix, next)
		block := append([]string{"*** " + kwTodo + " " + o.Title, ":PROPERTIES:", ":ID: " + id, ":FOUND_IN: " + o.FoundIn, ":END:"},
			context...)
		err = l.edit(file, func(lines []string) ([]string, error) {
			_, end, ok := section(lines, "Follow-ups")
			if !ok {
				return nil, refusal.New("%s has no `** Follow-ups` heading. Ask the operator to restore it as the plan-work skill describes, then run the command again", file)
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
		e, err := l.log(Event{Kind: "discovered", Item: id, Summary: o.Title}, now)
		r = Recorded{ID: id, File: file, Event: e}
		return err
	})
	return r, err
}
