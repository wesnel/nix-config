// Command plan-dash keeps the operator's status as Org, and exports it for
// the "Agent plans" page. See the usage text.
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
	"github.com/wesnel/agent-tools/internal/export"
	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/queue"
	"github.com/wesnel/agent-tools/internal/status"
)

const usage = `plan-dash: the operator's status, from GitHub and the plans.

  collect [--out FILE]
          write FILE (default $XDG_STATE_HOME/plan-dash/status.org),
          read-only: the operator's open pull requests (draft, CI, review
          decision, unresolved threads), the reviews asked of the operator
          (directly or of a team, since when, head and its CI, whether
          pr-loop holds a review of it) and this host's plans not halted (status, steps
          ready, open questions, routes owed)
  export  write the "Agent plans" page's database documents, as JSON, under
          $XDG_STATE_HOME/agent-plans/out: plans/<plan>.json for each plan
          of every host not halted (title, goal, status, host, updated,
          steps, decisions, open questions and their options),
          github/status.json (collect's pull requests and review requests,
          with titles) and meta/sync.json (when this host's plans last
          reached the notes remote, whether they changed since, when the
          notes repository last fetched); the documents of plans gone or
          halted are removed, those of plans that could not be read kept;
          and, outside out, the review requests listed, with their heads
  dispatch
          queue the reviews the page asked for: reads each document
          OWNER~NAME~N from out/dispatch/OWNER@NAME@N.json, as an
          ArtifactData read names it (or OWNER~NAME~N.json; two files for
          one document are refused), queues one whose state is
          "requested" when the last export listed OWNER/NAME#N, at the head
          it listed, with its triage (on unless false) and anyway; writes
          each queued review's state back there, and "refused" for the
          rest; removes files that name no such document; and starts
          agent-plans-queue.service unless $XDG_STATE_HOME/agent-plans/off
          exists. A request asked for again while queued stays as it is, a
          postponed one asked with anyway is queued past the quota gate,
          and a finished one is queued again. Of a document, only state,
          triage and anyway are read.

GitHub is read with gh, at most once a minute: a fetch is cached beside FILE
as github.json, and where one fails, the last good one is written, marked
stale; export shares collect's default cache. status.org holds only keys,
states, counts, times and the links built from them, never a title, branch,
login or comment; github.json holds titles. export's documents leave the
host: they hold only the fields named above, read from the plans directory
alone (of properties, only STATUS, RECOMMENDATION and ANSWER; of the log,
only event kinds and times), stripped of control and formatting
characters, with known secret shapes redacted and text and lists capped.

Prints JSON: the file or directory, when, whether GitHub's part is stale,
and counts. Exits 1 when GitHub or a plan could not be read, after writing
the rest. dispatch prints the keys it queued and queued anyway, the
document IDs it refused, how many files it removed, whether it started
the unit and whether the kill switch is on.
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && cli.IsHelp(args[0]) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if len(args) == 1 && args[0] == "export" {
		return exportDocs(stdout, stderr)
	}
	if len(args) == 1 && args[0] == "dispatch" {
		res, err := queue.Dispatch(queue.Default(), time.Now(), queue.StartUnit)
		return cli.Report("plan-dash", res, err, 0, stdout, stderr)
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

// exportDocs runs export. It takes no arguments: the dashboard session may
// run it, and nothing it is given can steer what is written where.
func exportDocs(stdout, stderr io.Writer) int {
	c, err := plans.Load()
	if err != nil {
		return cli.Report("plan-dash", nil, err, 0, stdout, stderr)
	}
	state := hostpaths.StateHome()
	dir := filepath.Join(state, "agent-plans", "out")
	now := time.Now()
	e, err := export.Collect(context.Background(), c, filepath.Join(state, "plan-dash", "github.json"), now)
	err = errors.Join(err, export.Write(dir, e))
	if e.GitHub.At != "" {
		var listed []queue.Request
		for _, r := range e.GitHub.Requests {
			listed = append(listed, queue.Request{Repo: r.Repo, Number: r.Number, Head: r.Head})
		}
		err = errors.Join(err, queue.WriteListed(queue.Default(), listed))
	}
	result := struct {
		Dir            string    `json:"dir"`
		At             time.Time `json:"at"`
		GitHub         bool      `json:"github"`
		Stale          bool      `json:"stale"`
		PullRequests   int       `json:"pullRequests"`
		ReviewRequests int       `json:"reviewRequests"`
		Plans          int       `json:"plans"`
	}{Dir: dir, At: now, GitHub: e.GitHub.At != "", Stale: e.GitHub.Stale, PullRequests: len(e.GitHub.Mine),
		ReviewRequests: len(e.GitHub.Requests), Plans: len(e.Plans)}
	return cli.Report("plan-dash", result, err, 0, stdout, stderr)
}
