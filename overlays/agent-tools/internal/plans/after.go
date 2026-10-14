package plans

import "strings"

// afterIDs is the AFTER property of props: the IDs a question or a part
// waits for.
func afterIDs(props map[string]string) []string {
	return strings.Fields(props["AFTER"])
}

// settled reports whether id is settled, by Org's rule: its keyword is one
// of its file's done keywords. A step's or a part's keyword is the one its
// log gives it, so an abandoned one settles too, as Org's blocker reads
// it. An ID the plan does not hold, a typo included, never settles: what
// waits for it keeps waiting, and status shows that.
func settled(list []item, events []Event) func(id string) bool {
	byID := map[string]item{}
	for _, it := range list {
		byID[it.ID] = it
	}
	return func(id string) bool {
		it, ok := byID[id]
		switch {
		case !ok:
			return false
		case stepPattern.MatchString(id) || partPattern.MatchString(id):
			return planKeywords.done[stepState(events, id)]
		}
		return it.Done
	}
}

// unsettled is the IDs of after that are not settled.
func unsettled(after []string, isSettled func(string) bool) []string {
	var out []string
	for _, id := range after {
		if !isSettled(id) {
			out = append(out, id)
		}
	}
	return out
}

// Askable is the questions of qs that wait for nothing.
func Askable(qs []Question) []Question {
	out := []Question{}
	for _, q := range qs {
		if len(q.Waiting) == 0 {
			out = append(out, q)
		}
	}
	return out
}

// Waiting is an item that waits for others.
type Waiting struct {
	ID string   `json:"id"`
	On []string `json:"on"`
}

// started reports whether part has begun: an event that starts or ends it
// or any of its items.
func started(part string, events []Event) bool {
	for _, e := range events {
		if (e.Item == part || strings.HasPrefix(e.Item, part+".")) &&
			(e.Kind == "started" || e.Kind == "done" || e.Kind == "verified") {
			return true
		}
	}
	return false
}
