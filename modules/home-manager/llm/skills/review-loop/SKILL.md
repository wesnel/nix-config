---
name: review-loop
description: Drive an open pull request, or a stack of them, draft or ready, through CI and automated review bots (Copilot, Qodo and any others configured) without supervision, then hand back to the operator for a signing pass. Use when asked to address, iterate on or loop over bot review feedback on a PR or a stack.
---

# Bot review loop

You coordinate. One `pr-owner` sub-agent per pull request owns that PR's
code; you own the decisions that span PRs, the restack, every push, every
reply and every review request.

Run the loop only if `PR_LOOP_SESSION=1` is set: only a session started the
loop's way has its sandbox and guards. Anywhere else, never run it in the
current session.

- **Claude Code:** start the loop as a separate session instead. From the
  repository, run this with the Bash tool's `run_in_background`, naming the
  branch you were pointed at:

  ```sh
  log="$(git rev-parse --path-format=absolute --git-common-dir)/pr-loop-session"
  claude-pr-loop -p "Run the review loop on <branch>." \
    --output-format stream-json --verbose \
    < /dev/null > "$log.jsonl" 2> "$log.err"
  ```

  Tell the operator it has started, and give them, with the repository's
  path filled in:
  - the logs, `$log.jsonl` and `$log.err`;
  - `cd <repository> && pr-loop watch`, which follows the session's
    progress from any terminal (`--from-start` for all of it; an interrupt
    stops only the watching);
  - `cd <repository> && pr-loop stop`, which ends the session, cleans up
    and writes a report of what is left.

  Leave the repository alone until it exits: the loop detaches the main
  worktree and works in `.pr-loop/`. When it exits, show
  `$(git rev-parse --git-common-dir)/pr-loop/report.md`, or the end of the
  log if there is no report.
- **Elsewhere:** stop and tell the operator to start `codex-pr-loop`, or
  `M-x wgn/eca-pr-loop` with the `pr-coordinator` agent. `pr-loop stop` in
  the repository also ends a `codex-pr-loop` session.

Commit as the `signing-pass` skill says: unsigned, never pushing or signing
yourself, with one signing pass at the end. In the loop's worktrees signing is
already off, so there the `-c commit.gpgsign=false` it asks for changes
nothing. Every write to GitHub goes through `pr-loop`, which needs no key
touch. `pr-loop` refuses anything outside the loop's PRs; a refusal is a
decision for the operator, so stop rather than route around it. On a PR that is
ready for review, every push, reply and review request also notifies its
reviewers and code owners.

Every commit the loop makes is a fixup of one of the PR's own commits, one
per review item, with the trailers the `review-change` skill's Answering
gives. Until the signing pass, `git log` on a branch is the record of what each
review item changed. `pr-loop push` refuses anything else, and the signing
pass squashes the fixups into the commits they name, leaving each PR with
exactly the commits it began with.

Never change Linear, post a top-level PR comment yourself, submit a review,
edit a PR, mark it ready for review, or merge. Put any of those you think
necessary in the report. The one top-level comment the loop makes is a
bot's trigger, which `pr-loop request` posts and later deletes.

## Scope

Each PR exists to deliver its Linear ticket. Measure every proposed change
against that goal, not against its size: a large change the ticket needs is
in scope, and a one-line improvement it does not need is not. Be skeptical of
anything a bot suggests that the ticket did not ask for, including
refactors, hardening, extra tests and fixes to code the PR did not touch.
Defer those with a short reason. A deferral costs nothing; scope creep costs
a reviewable PR.

## Sub-agents

Owners keep their memory in a notes file,
`$(git rev-parse --git-common-dir)/pr-loop/owners/<number>.md`, which they
read first and update last on every task. That makes each task
self-contained, so it works the same whether your harness can continue a
sub-agent (Claude Code's `SendMessage`) or only start a new one per task
(ECA's `spawn_agent`). Start owners in parallel where the harness allows.
If it has no sub-agents at all, do each owner's task yourself, one PR at a
time, keeping the same notes.

Run every sub-agent in the foreground, whatever the `coordinate-subagents`
skill says: in Claude Code never with `run_in_background`, and owners
working in parallel as several Agent calls in one message. Never end a turn
waiting on a sub-agent: a `claude -p` session stops whatever still runs
in the background 10 minutes after its last turn ends, killing an owner
mid-fix with no report. While the loop is active and `report.md` is
unwritten, the session's Stop hook blocks a stop once.

`claude-pr-loop` lifts that 10-minute limit, so a task left running in
the background keeps the session alive, with no limit, until it ends or
the operator runs `pr-loop stop`. That is all it does: it is no reason to
start a task in the background, and nothing says the session takes
another turn when such a task ends.

Anything an owner asks you, or that you need to ask an owner, goes in the
task you give next. Ask the owner rather than reading its code yourself
when you need to know where something belongs.

## Setup

1. `pr-loop stack` from the branch you were pointed at. If `linear` is false,
   stop and ask.
2. If its PRs chain by their bases but GitHub's stack (`github`) is null or
   leaves some out, run `pr-loop link` once with them, bottom first.
3. The main worktree must be clean. Detach it (`git switch --detach`) so each
   branch can be checked out in its owner's worktree.
4. `pr-loop begin`.
5. Write a scope statement per PR into its notes file: the goal from the PR
   description and its Linear ticket (read-only; the ticket ID is usually in
   the branch name, e.g. `agent-911` is `AGENT-911`), and what is explicitly
   not this PR's job.
6. `pr-loop worktrees` creates a worktree per PR at `.pr-loop/<number>`
   inside the repository, ignored by Git, with commit signing off in those
   worktrees only. Where the operator configured a setup command for the
   repository, it also runs that in each one, such as generating files the
   repository does not commit. If a setup fails, stop and report its output.
7. `pr-loop status`. Bot threads already on the PRs are answered before
   any bot is asked again: if any bot shows `unansweredThreads`, the first
   round runs steps 4 to 9, Triage to Reply and resolve, and the next round
   starts at step 1.

## Each round

At the start of the round, record every branch's tip, where the Review
step's range starts, and each child's fork point,
`git merge-base <child> <parent>`, which the restack needs once the parent
has changed.

1. **CI.** `pr-loop wait ci` (repeat while it exits 3), then `pr-loop ci`.
   If it exits 4 (`noChecks`), CI never reported on a head, and `pr-loop
   request` refuses until it does: stop and say so in the report. For every
   failure whose `onBase` is not `failed`, have the owner read
   `pr-loop logs <number> <check>` and either fix it or show it is not the
   PR's doing (a flaky test, an infrastructure fault). Waiving a failure is
   the operator's decision: if the owner shows one is not the PR's, stop and put
   the evidence in the report. While CI has failures of the PR's own, they
   are the round's only work: skip to step 5.
2. **Request reviews.** `pr-loop request`. It asks each configured bot that
   has neither reviewed the head nor been asked, and refuses while CI is
   unfinished or failing, or while a bot has the last word in an open
   thread; answer those through steps 4 to 9 first. Then
   `pr-loop wait reviews` (repeat while it exits 3); reviews take 5 to 15
   minutes.
3. **Status.** `pr-loop status`. Check the stop conditions below.
4. **Triage.** Have every owner triage its PR's unresolved bot threads
   (`pr-loop threads <number>`) without changing code. Then decide each
   thread's outcome as the `review-change` skill's Triage says, confirming a
   `belongs-in` with the owner of the PR that introduced the code. Threads
   started by people are always `needs-operator`, and the loop does not reply to
   them: `pr-loop reply` refuses.
5. **Fix.** Owners fix and commit in their own worktrees, one fixup per
   review item or CI failure, run the relevant tests and linters, and report
   each fixup and a draft reply per thread.
6. **Review**, once per round: for each PR with new fixups, one round of the
   `review-change` skill's fresh review loop. The range runs from the
   branch's tip at round start to the branch, in the PR's worktree; the goal
   statement is the PR's scope statement, and the items are the threads and
   checks the fixups answer. In a loop session `git fresh-review` exits 4, so
   the reviewer is a `reviewer` sub-agent. Give each finding to the PR's
   owner to settle. The owner's answers are not reviewed again this round; a
   finding still standing goes out with the push and into the report.
   Record every finding and its outcome in the PR's notes file under the
   round. Without sub-agents, skip this step and say so in the report.
7. **Restack**, bottom-up. Each child's owner runs
   `git rebase --onto <parent-branch> <child's recorded fork point> <branch>`
   in its worktree, resolves conflicts (the `restack-branches` skill covers
   them) and reruns its tests.
8. **Push.** `pr-loop push` pushes every changed branch, as the `signing-pass`
   skill says a stack is pushed, and records the round under
   `refs/pr-loop/rounds/<n>/<branch>`. If GitHub moved underneath it, stop.
9. **Reply and resolve**, after the push so replies can cite pushed commits:
   `pr-loop reply --from <file>` for each PR's drafted replies, which
   answer and resolve threads as their outcomes say.

Then start the next round.

## Stop conditions

Stop and hand back when any of these holds:

- Every bot has reviewed every head, Copilot's verdict is `approval`, and no
  bot thread is open except deferred and `needs-operator` ones.
- Copilot's verdict on a head is `closer-look` with no new comments: it is
  handing over to a person.
- Every bot reviewed every head without new comments, and the only open
  threads are deferred or need the operator.
- `pr-loop` refused a push, CI keeps failing
  after two rounds spent on it, or an owner showed a CI failure is not the
  PR's doing.
- A bot reopens a problem already fixed twice on the same thread.
- Six rounds have run.

## Hand-back

1. Once owners report clean trees, `pr-loop worktrees --remove`, and check
   the top branch out in the main worktree.
2. Write `$(git rev-parse --git-common-dir)/pr-loop/report.md` and show it.
   For each PR, include:
   - each bot's verdict
   - CI state and any accepted failures
   - commits added, and changed lines then and now
   - threads by outcome, with links
   - local review findings per round: fixed, rebutted, and any left standing

   Also list the Linear changes and follow-up tickets you propose but did
   not make. Point at the round snapshots for reviewing the code:
   `git range-diff refs/pr-loop/rounds/0/<branch>...refs/pr-loop/rounds/<n>/<branch>`
   shows everything the loop changed in a branch, and adjacent rounds show
   one round's work.
3. Notify the operator with the `notify` skill if it is available.
4. Hand over for the signing pass, showing what it will sign as the
   `signing-pass` skill describes. The loop's pass is `pr-loop sign`, not
   `git sign-pass`. The session's relay refuses `sign` and `end`, so the
   operator runs them in the repository. The loop's pass
   differs from the plain one:
   - it first squashes the fixups in a throwaway worktree, so a fixup that
     conflicts is named before any key touch;
   - it then squashes the fixups into the commits they name as it signs, and
     refuses unless each PR ends with exactly its original commits and
     unchanged files;
   - it pushes the whole stack over SSH unless given `--no-push`.

   Afterwards `pr-loop end` deletes the loop's trigger comments and its
   state, and `--prune` drops the backups and snapshots.
