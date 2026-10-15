package plans

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// Reference keeps address in the plan's References, with note, what it
// was used for and which decisions it fed: a new entry, or a new note for
// one already there. It logs a referenced event, and returns no event when
// there was nothing to change: the entry exists and note is empty or its
// note already.
func (p Plan) Reference(address, note string, now time.Time) (Event, error) {
	if err := p.checkOwn(); err != nil {
		return Event{}, err
	}
	note = strings.TrimSpace(note)
	if u, err := url.Parse(address); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		strings.ContainsAny(address, " \t\n\r") {
		return Event{}, refusal.New("%q is not an http or https URL. Pass the address as the operator gave it, as `agent-plan reference PLAN https://...`", address)
	}
	if err := checkLine("note", note); err != nil {
		return Event{}, err
	}
	var e Event
	err := p.withLock(func(l *lockedPlan) error {
		changed := false
		err := l.edit(p.file(""), func(lines []string) ([]string, error) {
			out, ok, err := setReference(lines, address, note)
			changed = ok
			return out, err
		})
		if err != nil || !changed {
			return err
		}
		e, err = l.log(Event{Kind: "referenced", Item: p.Name, Summary: address}, now)
		return err
	})
	return e, err
}

// setReference is lines with address's entry in References, its note set
// when note is given, and whether that changed anything.
func setReference(lines []string, address, note string) ([]string, bool, error) {
	start, end, ok := section(lines, "References")
	if !ok {
		return nil, false, refusal.New("the plan has no `** References` heading. Restore it as the plan-work skill describes, then run the command again")
	}
	for i := start + 1; i < end; i++ {
		if strings.TrimSpace(lines[i]) != ":URL: "+address {
			continue
		}
		at := i
		for level(lines[at]) == 0 {
			at--
		}
		if note == "" {
			return lines, false, nil
		}
		body := at + 1
		for body < len(lines) && strings.TrimSpace(lines[body]) != ":END:" {
			body++
		}
		body++
		next := subtreeEnd(lines, at)
		if next == len(lines) && lines[next-1] == "" {
			next--
		}
		if strings.Join(lines[body:next], "\n") == note {
			return lines, false, nil
		}
		out := slices.Concat(lines[:body], []string{note}, lines[next:])
		return out, true, nil
	}
	entry := []string{"*** " + address, ":PROPERTIES:", ":URL: " + address, ":END:"}
	if note != "" {
		entry = append(entry, note)
	}
	// A section ending the file ends at its final empty line.
	if end == len(lines) && lines[end-1] == "" {
		end--
	}
	return slices.Concat(lines[:end], entry, lines[end:]), true, nil
}

// promptURL matches an http(s) address in prose, up to a space, a quote,
// a bracket or an angle bracket.
var (
	promptURL     = regexp.MustCompile(`https?://[^\s<>"'\x60()\[\]{}]+`)
	trailingPunct = ".,;:!?"
)

// promptReferences are the http(s) addresses in prompt, in order, each
// once, without the punctuation of the sentence around them.
func promptReferences(prompt string) []string {
	var out []string
	for _, m := range promptURL.FindAllString(prompt, -1) {
		m = strings.TrimRight(m, trailingPunct)
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// recordReferences keeps each address in the operator's prompt as a reference
// of the plan, with no note for now. Its errors are dropped: a prompt is
// never held up by its plan.
func recordReferences(p Plan, prompt string, now time.Time) {
	for _, address := range promptReferences(prompt) {
		_, _ = p.Reference(address, "", now)
	}
}
