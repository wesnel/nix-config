package cli

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	const usage = "usage: tool [--push]\n"
	for _, tc := range []struct {
		args           []string
		code           int
		ok             bool
		stdout, stderr string
	}{
		{args: []string{"--push"}, ok: true},
		{args: []string{"--help"}, stdout: usage},
		{args: []string{"-h"}, stdout: usage},
		{args: []string{"--bogus"}, code: 2, stderr: "flag provided but not defined: -bogus\n" + usage},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			flags := flag.NewFlagSet("tool", flag.ContinueOnError)
			flags.Bool("push", false, "")
			code, ok := Parse(flags, tc.args, usage, &stdout, &stderr)
			if code != tc.code || ok != tc.ok {
				t.Errorf("%v: got (%d, %v), want (%d, %v)", tc.args, code, ok, tc.code, tc.ok)
			}
			if stdout.String() != tc.stdout {
				t.Errorf("%v: stdout %q, want %q", tc.args, stdout.String(), tc.stdout)
			}
			if stderr.String() != tc.stderr {
				t.Errorf("%v: stderr %q, want %q", tc.args, stderr.String(), tc.stderr)
			}
			if strings.Count(stdout.String()+stderr.String(), usage) > 1 {
				t.Errorf("%v: usage printed more than once", tc.args)
			}
		})
	}
}
