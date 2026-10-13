// Package refusal marks a failed policy check, which the commands report
// with exit status 2 so a caller can tell it from a failure to run.
package refusal

import "fmt"

// Refusal is a failed policy check: the caller must stop or change course,
// not retry.
type Refusal struct{ msg string }

func (r Refusal) Error() string { return r.msg }

// New returns a Refusal with a formatted message.
func New(format string, args ...any) error { return Refusal{fmt.Sprintf(format, args...)} }
