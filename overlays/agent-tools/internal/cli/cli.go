// Package cli reports a command's result the same way for every tool: JSON on
// stdout, exit 2 for a refusal, 1 for any other failure.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// Report writes result, or err prefixed with the tool's name, and returns the
// exit code; code is used when there is no error.
func Report(name string, result any, err error, code int, stdout, stderr io.Writer) int {
	var r refusal.Refusal
	switch {
	case errors.As(err, &r):
		fmt.Fprintf(stderr, "%s: refused: %s\n", name, r)
		return 2
	case err != nil:
		fmt.Fprintf(stderr, "%s: %s\n", name, err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(stderr, "%s: %s\n", name, err)
		return 1
	}
	return code
}
