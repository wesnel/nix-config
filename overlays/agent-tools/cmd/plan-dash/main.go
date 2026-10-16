// Command plan-dash keeps the operator's status as Org. See the usage text.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/status"
)

const usage = `plan-dash: the operator's status, from GitHub and this host's plans.

  collect [--out FILE]
          write FILE (default $XDG_STATE_HOME/plan-dash/status.org),
          read-only: the operator's open pull requests (draft, CI, review
          decision, unresolved threads), the reviews asked of the operator
          (directly or of a team, since when, head and its CI, whether
          pr-loop holds a review of it) and this host's plans not halted (status, steps
          ready, open questions, routes owed)

GitHub is read with gh, at most once a minute: a fetch is cached beside FILE
as github.json, and where one fails, the last good one is written, marked
stale. status.org holds only keys, states, counts, times and the links built
from them, never a title, branch, login or comment; github.json holds titles.

Prints JSON: the file, when, whether GitHub's part is stale, and counts.
Exits 1 when GitHub or a plan could not be read, after writing the rest.
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && cli.IsHelp(args[0]) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if len(args) == 0 || args[0] != "collect" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	flags := flag.NewFlagSet("collect", flag.ContinueOnError)
	out := flags.String("out", filepath.Join(hostpaths.StateHome(), "plan-dash", "status.org"), "")
	if code, ok := cli.Parse(flags, args[1:], usage, stdout, stderr); !ok {
		return code
	}
	if flags.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	c, err := plans.Load()
	if err != nil {
		return cli.Report("plan-dash", nil, err, 0, stdout, stderr)
	}
	s, err := status.Collect(context.Background(), c, filepath.Join(filepath.Dir(*out), "github.json"), time.Now())
	err = errors.Join(err, status.Write(*out, s.Org()))
	result := struct {
		File           string    `json:"file"`
		At             time.Time `json:"at"`
		GitHub         bool      `json:"github"`
		Stale          bool      `json:"stale"`
		PullRequests   int       `json:"pullRequests"`
		ReviewRequests int       `json:"reviewRequests"`
		Plans          int       `json:"plans"`
	}{File: *out, At: s.At, GitHub: s.GitHub != nil, Stale: s.Stale, Plans: len(s.Plans)}
	if s.GitHub != nil {
		result.PullRequests, result.ReviewRequests = len(s.GitHub.Mine), len(s.GitHub.Requests)
	}
	return cli.Report("plan-dash", result, err, 0, stdout, stderr)
}
