// Package refusal marks a failed policy check, which the commands report
// with exit status 2 so a caller can tell it from a failure to run.
package refusal

import (
	"fmt"
	"regexp"
)

// Refusal is a failed policy check: the caller must stop or change course,
// not retry.
type Refusal struct{ msg string }

func (r Refusal) Error() string { return r.msg }

// New returns a Refusal with a formatted message.
func New(format string, args ...any) error { return Refusal{fmt.Sprintf(format, args...)} }

// handOff is what the rest of a reason, after the sentence saying why, must
// contain: a command in backticks, or the step handed to the operator or the
// report.
var handOff = regexp.MustCompile("`|(?i:operator|report)")

// SaysInstead reports whether a refusal's or denial's reason goes on, after
// the sentence saying why, to say what to do instead. A reason that only
// says no sends an agent looking for another way through, so tests hold
// every reason an agent sees to this.
func SaysInstead(reason string) bool {
	quoted := false
	for i := 0; i+1 < len(reason); i++ {
		switch {
		case reason[i] == '`':
			quoted = !quoted
		case !quoted && (reason[i] == '.' || reason[i] == ';') && reason[i+1] == ' ':
			return handOff.MatchString(reason[i+2:])
		}
	}
	return false
}
