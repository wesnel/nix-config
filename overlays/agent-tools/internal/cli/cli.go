// Package cli reports a command's result the same way for every tool: JSON on
// stdout, exit 2 for a refusal, 1 for any other failure.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// IsHelp reports whether arg, a command's first argument, asks for its usage.
func IsHelp(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

// Parse parses args into flags. When the command should not go on, it returns
// false with the exit code: 0 after printing usage on stdout for -h or --help,
// 2 after printing the error and usage on stderr for a bad flag.
func Parse(flags *flag.FlagSet, args []string, usage string, stdout, stderr io.Writer) (int, bool) {
	flags.SetOutput(stderr)
	flags.Usage = func() {}
	err := flags.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(stdout, usage)
		return 0, false
	case err != nil:
		fmt.Fprint(stderr, usage)
		return 2, false
	}
	return 0, true
}

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
