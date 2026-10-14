package plans

import (
	"regexp"
	"strings"
)

// The ID patterns are the contract with every other reader of plan files:
// emacs-config's plan views select entries by them. Change one only
// together with those, and with the plan-work skill, which documents them.
var (
	partPattern     = regexp.MustCompile(`^P[0-9]+$`)
	stepPattern     = regexp.MustCompile(`^(P[0-9]+\.)?S[0-9]+$`)
	questionID      = regexp.MustCompile(`^(P[0-9]+\.)?Q[0-9]+$`)
	decisionPattern = regexp.MustCompile(`^(P[0-9]+\.)?D[0-9]+$`)
)

// idOf matches the IDs with prefix, such as "P1." or "", and letter, such
// as "Q", capturing the number.
func idOf(prefix, letter string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + letter + `([0-9]+)$`)
}

// keywordSet is the TODO keywords an Org file defines, and which of them
// are done.
type keywordSet struct {
	all, done map[string]bool
}

// orgDefault is what Org uses for a file that defines no keywords.
var orgDefault = keywordSet{all: set("TODO", "DONE"), done: set("DONE")}

var (
	todoLine   = regexp.MustCompile(`(?i)^#\+(?:SEQ_|TYP_)?TODO:(.*)$`)
	decoration = regexp.MustCompile(`\(.*\)$`)
)

// fileKeywords reads the #+TODO, #+SEQ_TODO and #+TYP_TODO lines of an Org
// file, as Org does: words before a bar are open and after it done, and
// without a bar the last word alone is done. Decorations such as `(@)` are
// not part of a keyword.
func fileKeywords(text string) keywordSet {
	k := keywordSet{all: map[string]bool{}, done: map[string]bool{}}
	for _, line := range strings.Split(text, "\n") {
		m := todoLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		words := strings.Fields(m[1])
		bar := -1
		for i, w := range words {
			if w == "|" {
				bar = i
			}
		}
		for i, w := range words {
			if w == "|" {
				continue
			}
			w = decoration.ReplaceAllString(w, "")
			k.all[w] = true
			if (bar >= 0 && i > bar) || (bar < 0 && i == len(words)-1) {
				k.done[w] = true
			}
		}
	}
	if len(k.all) == 0 {
		return orgDefault
	}
	return k
}
