package plans

import (
	"regexp"
	"strings"
)

var decisionPattern = regexp.MustCompile(`^(P[0-9]+\.)?D[0-9]+$`)

// afterIDs is the AFTER property of props: the IDs a question or a part
// waits for.
func afterIDs(props map[string]string) []string {
	return strings.Fields(props["AFTER"])
}

// settled reports whether id is settled, by the plan's items and log: a
// question once decided, a decision at once, and a part or step once a done
// event is logged for it. An ID the plan never settles, a typo included,
// keeps whatever waits for it waiting, and status shows that.
func settled(list []item, events []Event) func(id string) bool {
	done := map[string]bool{}
	for _, it := range list {
		if q := it.Props["QUESTION"]; q != "" {
			done[q] = true
		}
		if decisionPattern.MatchString(it.ID) {
			done[it.ID] = true
		}
	}
	for _, e := range events {
		if e.Kind == "done" {
			done[e.Item] = true
		}
	}
	return func(id string) bool { return done[id] }
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
