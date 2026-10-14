---
name: review-loop
description: Drive an open pull request, or a stack of them, draft or ready, through CI and automated review bots (Copilot, Qodo and any others configured) without supervision, then hand back to Wesley for a signing pass. Use when asked to address, iterate on or loop over bot review feedback on a PR or a stack.
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
  log="$(git rev-parse --git-common-dir)/pr-loop-session"
  claude-pr-loop -p "Run the review loop on <branch>." \
    --output-format stream-json --verbose \
    < /dev/null > "$log.jsonl" 2> "$log.err"
  ```

  Tell Wesley it has started and where the logs are. Leave the repository
  alone until it exits: the loop detaches the main worktree and works in
  `.pr-loop/`. When it exits, show
  `$(git rev-parse --git-common-dir)/pr-loop/report.md`, or the end of the
  log if there is no report.
- **Elsewhere:** stop and tell Wesley to start `codex-pr-loop`, or
  `M-x wgn/eca-pr-loop` with the `pr-coordinator` agent.

Commit as the `signing-pass` skill says: unsigned, never pushing or signing
yourself, with one signing pass at the end. In the loop's worktrees signing is
already off, so there the `-c commit.gpgsign=false` it asks for changes
nothing. Every write to GitHub goes through `pr-loop`, which needs no key
touch. `pr-loop` refuses anything outside the loop's PRs; a refusal is a
decision for Wesley, so stop rather than route around it. On a PR that is
ready for review, every push, reply and review request also notifies its
reviewers and code owners.

Every commit the loop makes is also a fixup of one of the PR's own commits,
one per review item, carrying the review's details as trailers:

```sh
git commit --fixup=<the PR commit this belongs in> \
  --trailer "Review-Thread: <thread URL>" \
  --trailer "Review-Bot: <bot>" \
  --trailer "Review-Round: <round>"
```

Until the signing pass, `git log` on a branch is the record of what each
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

Anything an owner asks you, or that you need to ask an owner, goes in the
task you give next. Ask the owner rather than reading its code yourself
when you need to know where something belongs.

A `reviewer` sub-agent reviews a PR's fixups before they are pushed. Start
a new one for every review, never continue one, and give it nothing of the
owner's report: its use is that it did not write the fix. It only reads, and
keeps no notes.

## Setup

1. `pr-loop stack` from the branch you were pointed at. If `linear` is false,
   stop and ask.
2. The main worktree must be clean. Detach it (`git switch --detach`) so each
   branch can be checked out in its owner's worktree.
3. `pr-loop begin`.
4. Write a scope statement per PR into its notes file: the goal from the PR
   description and its Linear ticket (read-only; the ticket ID is usually in
   the branch name, e.g. `agent-911` is `AGENT-911`), and what is explicitly
   not this PR's job.
5. `pr-loop worktrees` creates a worktree per PR at `.pr-loop/<number>`
   inside the repository, ignored by Git, with commit signing off in those
   worktrees only. Where Wesley configured a setup command for the
   repository, it also runs that in each one, such as generating files the
   repository does not commit. If a setup fails, stop and report its output.

## Each round

Record every branch's tip at the start of the round; the restack needs the
old parent tips.

1. **CI.** `pr-loop wait ci` (repeat while it exits 3), then `pr-loop ci`.
   If it exits 4 (`noChecks`), CI never reported on a head, and `pr-loop
   request` refuses until it does: stop and say so in the report. For every
   failure whose `onBase` is not `failed`, have the owner read
   `pr-loop logs <number> <check>` and either fix it or show it is not the
   PR's doing (a flaky test, an infrastructure fault). Waiving a failure is
   Wesley's decision: if the owner shows one is not the PR's, stop and put
   the evidence in the report. While CI has failures of the PR's own, they
   are the round's only work: skip to step 5.
2. **Request reviews.** `pr-loop request`. It asks each configured bot that
   has neither reviewed the head nor been asked, and refuses while CI is
   unfinished or failing. Then `pr-loop wait reviews` (repeat while it exits
   3); reviews take 5 to 15 minutes.
3. **Status.** `pr-loop status`. Check the stop conditions below.
4. **Triage.** Have every owner triage its PR's unresolved bot threads
   (`pr-loop threads <number>`) without changing code. Then decide each
   thread:
   - **Fix** in the PR whose diff introduced the code. When a comment on one
     PR is about code another PR introduced, confirm with that PR's owner
     first.
   - **Duplicate**: the same problem raised on several PRs or by several bots
     is fixed once; the other threads point at that fix.
   - **Decline**: the bot is wrong. The owner must demonstrate it from the
     code or a test, not assert it.
   - **Defer**: real, but outside the scope statement.
   - **Needs Wesley**: a product or design call, or anything touching another
     team's code.

   Threads started by people are always "needs Wesley". Do not reply to them.
5. **Fix.** Owners fix and commit in their own worktrees, one fixup per
   review item or CI failure, run the relevant tests and linters, and report
   commit SHAs and a draft reply per thread.
6. **Review**, once per round. For each PR with new fixups, start a
   `reviewer` with the range from the branch's tip at round start to the
   branch, the worktree to read it in, the scope statement as its goal
   statement, and the threads and checks the fixups answer as its items. Give each finding to the PR's owner, which fixes it in a
   further fixup for the same review item or rebuts it with evidence. Judge a
   rebuttal as you would a decline. The owner's answers are not reviewed
   again this round; a finding still standing goes out with the push and into
   the report. Record every finding and its outcome (fixed in which commit,
   rebutted with what evidence, or standing) in the PR's notes file under the
   round. Without sub-agents, skip this step and say so in the report.
7. **Restack**, bottom-up. Each child's owner runs
   `git rebase --onto <parent-branch> <parent's tip at round start> <branch>`
   in its worktree, resolves conflicts (the `restack-branches` skill covers
   them) and reruns its tests.
8. **Push.** `pr-loop push` pushes every changed branch at once and records
   the round under `refs/pr-loop/rounds/<n>/<branch>`. If GitHub moved
   underneath it, stop.
9. **Reply and resolve**, after the push so replies can cite pushed commits.
   `pr-loop reply <thread> --body-file <file>` on every fixed, duplicate,
   declined and deferred thread: what changed and in which commit, or why
   not, in two or three sentences. Then `pr-loop resolve <thread>` for
   declines with demonstrated evidence, and for fixes and duplicates whose
   fix landed, except on a bot whose `resolvesOwnThreads` is true (Qodo):
   it resolves its own threads when it re-reviews, so leave those to it.
   Deferred threads stay open. `pr-loop reply --from <file>` does a round's
   replies and resolves in one go; `pr-loop --help` gives its format.

Then start the next round.

## Stop conditions

Stop and hand back when any of these holds:

- Every bot has reviewed every head, Copilot's verdict is `approval`, and no
  bot thread is open except deferred and needs-Wesley ones.
- Copilot's verdict on a head is `closer-look` with no new comments: it is
  handing over to a person.
- Every bot reviewed every head without new comments, and the only open
  threads are deferred or need Wesley.
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
   - threads fixed, declined, deferred and needing Wesley, with links
   - local review findings per round: fixed, rebutted, and any left standing

   Also list the Linear changes and follow-up tickets you propose but did
   not make. Point at the round snapshots for reviewing the code:
   `git range-diff refs/pr-loop/rounds/0/<branch>...refs/pr-loop/rounds/<n>/<branch>`
   shows everything the loop changed in a branch, and adjacent rounds show
   one round's work.
3. Notify Wesley with the `notify` skill if it is available.
4. Hand over for the signing pass, showing what it will sign as the
   `signing-pass` skill describes. The loop's pass is `pr-loop sign`, not
   `git sign-pass`. The session's relay refuses `sign` and `end`, so Wesley
   runs them in the repository. The loop's pass
   differs from the plain one:
   - it first squashes the fixups in a throwaway worktree, so a fixup that
     conflicts is named before any key touch;
   - it then squashes the fixups into the commits they name as it signs, and
     refuses unless each PR ends with exactly its original commits and
     unchanged files;
   - it pushes the whole stack over SSH unless given `--no-push`.

   Afterwards `pr-loop end` deletes the loop's trigger comments and its
   state, and `--prune` drops the backups and snapshots.
