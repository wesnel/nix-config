// Command plan-dash prepares plan mirrors and exports status documents.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/export"
	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/plans"
)

const usage = `plan-dash: the operator's status, from GitHub and the plans.

  snapshot [PLAN]  prepare the current session's read-only plan mirror;
                   without PLAN, require one exact session match. Print
                   the immutable HTML file, digest and owned destination.
  snapshot --open prepare all open items for the Claude aggregate's owner.
  refresh          collect all open plan items and update the configured
                   Space page under one local lock. Uses no model turn or
                   GitHub status lookup; bounded and safe to retry unless
                   a prior publication outcome needs reconciliation.
  claim            claim the configured Claude aggregate for this session.
  release          release this session's aggregate claim after confirmed
                   completion. An uncertain write keeps its claim.
  remind           lifecycle hook: remind an owning session at checkpoints.
  record           post-tool hook: record only a matched native publication
                   receipt. Not a command for sessions to invoke directly.

  export  write the "Agent plans" page's database documents, as JSON, under
          $XDG_STATE_HOME/agent-plans/out: plans/<plan>.json for each plan
          of every host not halted (title, goal, status, host, updated,
          the title of its workstream, which the page groups plans by,
          steps, decisions, open questions and their options, and the
          IDs and titles of open follow-ups),
          github/status.json (the operator's open pull requests, with
          draft, CI, review decision and unresolved threads, and the
          reviews asked of the operator, directly or of a team, with since
          when, head and its CI and whether pr-loop holds a review of it,
          all with titles, and the one of each list each is stacked on),
          meta/sync.json (when this host's plans last reached the notes
          remote, whether they changed since, when the notes repository
          last fetched, how many plans could not be read) and
          today/digest.json (the demo digest of the newest day of the
          last week with one, from digests/YYYY/ in the notes repository); the
          documents of plans gone or halted are removed, those of plans
          that could not be read kept, and so is today/digest.json where a
          digest could not be read

GitHub is read with gh, at most once a minute: a fetch is cached as
$XDG_STATE_HOME/plan-dash/github.json, which holds titles, and where one
fails, the last good one is written, marked stale. export's documents leave
the host: they hold only the fields named above and schema, the documents'
version; read from the plans directory alone (of properties, only STATUS,
RECOMMENDATION, ANSWER and WORKSTREAM, whose workstream file gives only its
title; of the log, only event kinds and times) but for
the digests, stripped of control and formatting
characters, with known secret shapes redacted and text and lists capped.

Prints JSON: the directory, when, whether GitHub's part is stale, counts,
and the documents it wrote, as COLLECTION/ID. Exits 1 when GitHub or a
plan could not be read, after writing the rest.
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && cli.IsHelp(args[0]) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if len(args) >= 1 && args[0] == "snapshot" && len(args) <= 2 {
		return snapshot(args[1:], stdout, stderr)
	}
	if len(args) == 1 {
		switch args[0] {
		case "refresh":
			return refresh(stdout, stderr)
		case "claim":
			return mirrorClaim(false, stdout, stderr)
		case "release":
			return mirrorClaim(true, stdout, stderr)
		case "remind":
			return mirrorRemind(os.Stdin, stdout)
		case "record":
			return mirrorRecord(os.Stdin, stdout, stderr)
		}
	}
	if len(args) == 1 && args[0] == "export" {
		return exportDocs(stdout, stderr)
	}
	fmt.Fprint(stderr, usage)
	return 2
}

// exportDocs writes the configured status documents to local state.
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
	result := struct {
		Dir            string    `json:"dir"`
		At             time.Time `json:"at"`
		GitHub         bool      `json:"github"`
		Stale          bool      `json:"stale"`
		PullRequests   int       `json:"pullRequests"`
		ReviewRequests int       `json:"reviewRequests"`
		Plans          int       `json:"plans"`
		// Documents are those written, as collection/ID, for the session
		// to put on the page.
		Documents []string `json:"documents"`
	}{Dir: dir, At: now, GitHub: e.GitHub.At != "", Stale: e.GitHub.Stale, PullRequests: len(e.GitHub.Mine),
		ReviewRequests: len(e.GitHub.Requests), Plans: len(e.Plans), Documents: []string{"github/status", "meta/sync"}}
	if !e.TodayFailed {
		result.Documents = append(result.Documents, "today/digest")
	}
	for _, p := range e.Plans {
		result.Documents = append(result.Documents, "plans/"+p.ID)
	}
	return cli.Report("plan-dash", result, err, 0, stdout, stderr)
}
