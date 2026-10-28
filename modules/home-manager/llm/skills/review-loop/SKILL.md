---
name: review-loop
description: Drive the operator's open pull requests, or a stack of them, draft or ready, through CI and review, in the mode the operator started the session in. Bots mode answers the review bots (Copilot, Qodo and any others configured) and pushes; people mode drafts answers to people's threads for the operator to post. Hand back for a signing pass.
disable-model-invocation: true
---

# Review loop

The operator starts this session with
`review-loop --harness claude|codex --bots|--people PR...`, choosing one
harness and one mode. The launcher sets `PR_LOOP_MODE` to `bots` or
`people` and starts that harness with this skill and the PRs it names. It is
an ordinary session, held by the same sandbox and guard as any other, and
`pr-loop` takes its writes to GitHub as the mode says:

- **bots:** `pr-loop request`, `reply`, `resolve` and `push` run; threads
  are a configured bot's alone.
- **people:** `pr-loop draft` writes the answer to a thread into the PR's
  review record, and `pr-loop push` stops: the operator reads the fixups
  and the drafts, pushes with `git sign-pass --push` and posts the drafts
  with `pr-loop post PR` from a terminal of their own.

If `PR_LOOP_MODE` is unset, this is not a loop session: tell the operator
to start one with `review-loop`, and stop.

Commit as the `signing-pass` skill says. Every write to GitHub goes through
`pr-loop`; when it stops a command, that is a decision for the operator, so
stop rather than route around it. On a PR that is ready for review, every
push, reply and review request also notifies its reviewers and code owners.

Every commit the loop makes is a fixup of one of the PR's own commits, one
per review item, with the trailers the `review-change` skill's Answering
gives. On a ready PR, `pr-loop push` takes nothing else, and the signing
pass squashes the fixups into the commits they name, leaving each PR with
exactly the commits it began with.

Never change Linear, post a top-level PR comment yourself, submit a review,
edit a PR, mark it ready for review, or merge. Put any of those you think
necessary in the hand-back. The one top-level comment the loop makes is a
bot's trigger, which `pr-loop request` posts and `pr-loop end` deletes.

## Scope

Each PR exists to deliver its Linear ticket. Its scope statement (Setup
below) is its goal statement, against which every proposed change is
measured as the `review-change` skill's goal statement says.

## Setup

1. `pr-loop stack` from each PR's branch. If `linear` is false, stop and
   ask. If its PRs chain by their bases but GitHub's stack (`github`) is
   null or leaves some out, run `pr-loop link` once with them, bottom first.
2. Write a scope statement per PR into the plan (the `plan-work` skill):
   the goal from the PR description and its Linear ticket (read-only; the
   ticket ID is usually in the branch name, e.g. `agent-911` is
   `AGENT-911`), and what is explicitly not this PR's job.
3. Work on each PR's branch in a worktree under the session's temporary
   directory, which both harnesses allow the sandbox to write,
   made with `git worktree add`, with `git config --worktree
   commit.gpgsign false` in it. In each new worktree run `pr-loop setup`,
   which runs the setup command the operator configured for the repository,
   such as generating files it does not commit. If a setup fails, stop and
   hand back its output.
4. `pr-loop threads <number>` for each PR, which also writes the PR's
   review record.

## Bots mode: each round

At the start of the round, record every branch's tip, where the Review
step's range starts, and each child's fork point,
`git merge-base <child> <parent>`, which the restack needs once the parent
has changed. Bot threads already open on the PRs, such as Qodo's review on
opening, are answered before any bot is asked again: when `pr-loop status`
shows a bot's `unansweredThreads`, the first round runs steps 4 to 9 and the
next one starts at step 1.

1. **CI.** `pr-loop wait ci PR`, run in the background or under Monitor
   (repeat while it exits 3), then `pr-loop ci PR`. If it exits 4
   (`noChecks`), CI never reported on a head, and no bot can be asked until
   it does: stop and say so in the hand-back. For every failure whose
   `onBase` is not `failed`, read `pr-loop logs <number> <check>` and either
   fix it or show it is not the PR's doing (a flaky test, an infrastructure
   fault). Waiving a failure is the operator's decision: put the evidence in
   the hand-back with `pr-loop request --accept-failure CHECK PR` for them.
   While CI has failures of the PR's own, they are the round's only work:
   skip to step 5.
2. **Request reviews.** `pr-loop request`, after the first green CI on a
   head and after every push once CI is green. It asks each configured bot
   that has not reviewed the head and was not asked already, drafts
   included, once every thread the bot started has an answer. Then
   `pr-loop wait reviews` (repeat while it exits 3); reviews take 5 to 15
   minutes.
3. **Status.** `pr-loop status`. Check the stop conditions below.
4. **Triage.** `pr-loop threads <number>` lists each PR's unresolved
   threads. Decide each bot thread's outcome as the `review-change` skill's
   Triage says, against the code. A thread a person commented in goes in
   the hand-back with its proposed answer: in bots mode the loop answers
   bots alone.
5. **Fix.** One fixup per review item or CI failure, in the PR's worktree,
   with the relevant tests and linters run, and a drafted reply per thread
   in the format `pr-loop reply --from` reads. A reply is read by people
   who have never seen the loop: it links other threads by URL and commits
   by short SHA, as the `pull-requests` skill says, and `pr-loop reply`
   refuses one naming a thread's ID or key, a plan ID or the loop's own
   terms.
6. **Review**, once per round: for each PR with new fixups, a fresh review
   over the range from the
   branch's tip at round start to the branch. The goal statement is the
   PR's scope statement, and the items are the threads and checks the
   fixups answer. Start a fresh read-only reviewer sub-agent in the
   selected harness: Claude Code's `adversarial-reviewer`, or a Codex
   sub-agent given the goal, range and items. Ask it to inspect every
   changed hunk, check each finding against code or a reproduction, and
   report its evidence and anything it could not verify, without editing.
   Codex reviews use a separate context in the same model family; report
   that limitation. Ask the general reviewer to identify the focused
   reviews the change needs, with reasons: security for changes to trust
   boundaries, compat for interfaces and stored state, upgrade for version
   bumps. For each needed focus, give a fresh read-only sub-agent
   the prompt from `git delegate --print-prompt --reviewer TYPE` with the
   same goal and range. Use `git delegate` only with `--print-prompt` in
   loop sessions; its live runners can start a different harness.
   Settle findings as the `review-change` skill's Triage says.
   A `compat` review
   the change is routed to also takes `--description FILE`, the PR's
   description as it stands. A finding still standing goes out with the
   push and into the hand-back.
7. **Restack**, bottom-up:
   `git rebase --onto <parent-branch> <child's recorded fork point> <branch>`
   for each child, resolving conflicts as the `restack-branches` skill says,
   with its tests run again.
8. **Push.** `pr-loop push BRANCH...`, bottom up, as the `signing-pass`
   skill says a stack is pushed. A rebased branch no longer contains its
   head on GitHub, so name that head: `--lease BRANCH=SHA`, the `remoteOid`
   `pr-loop stack` shows. If GitHub moved underneath it, stop.
9. **Reply and resolve**, after the push so replies can cite pushed
   commits: `pr-loop reply --from <file>` for each PR's drafted replies,
   which answer and resolve threads as their outcomes say.

Then start the next round.

## People mode

1. **Triage.** For each PR, triage every unresolved thread a person
   commented in, from `pr-loop threads <number>`, as the `review-change`
   skill's Triage says, against the code.
2. **Fix.** Each fix is a local fixup, in the PR's worktree, with the
   trailers Answering gives and the relevant tests run; restack a stack's
   children as bots mode's step 7 says. Push nothing.
3. **Draft.** Each answer goes into the PR's review record with
   `pr-loop draft THREAD --body-file FILE --outcome OUTCOME`, the outcome as
   Triage settled it. The file lies under the working directory, and is
   removed once drafted.
4. **Hand back** with the fixups (`git log --format='%h %G? %s'` per branch)
   and the drafts, which review-guide shows from the record too. The
   operator reads both, pushes with `git sign-pass --push`, and posts with
   `pr-loop post PR` from a terminal; post resolves the threads answered
   fix or rebut, and once every person's thread has an answer, asks those
   people to review again.

## Stop conditions

In bots mode, stop and hand back when any of these holds:

- Every bot has reviewed every head, Copilot's verdict is `approval`, and no
  bot thread is open except deferred and `needs-operator` ones.
- Copilot's verdict on a head is `closer-look` with no new comments: it is
  handing over to a person.
- Every bot reviewed every head without new comments, and the only open
  threads are deferred or need the operator.
- `pr-loop` stopped a push, CI keeps failing after two rounds spent on it,
  or a CI failure was shown not to be the PR's doing.
- A bot reopens a problem already fixed twice on the same thread.
- Six rounds have run.

## Hand-back

1. Remove the worktrees you made, once each is clean, and check the top
   branch out in the main worktree.
2. Report, for each PR:
   - each bot's verdict;
   - CI state and any failures for the operator to accept;
   - the commits added;
   - threads by outcome, with links, and people's threads with their
     proposed or drafted answers;
   - review findings per round: fixed, rebutted, and any left standing.

   Also list the Linear changes and follow-up tickets you propose but did
   not make.
3. Run `pr-loop sign-check` on the top branch. It squashes the fixups
   unsigned in a throwaway worktree and says whether each PR would end with
   exactly its own commits and unchanged files; settle what it names before
   handing over. Then hand over the signing pass as the `signing-pass`
   skill's Handing over says, naming the stack's top branch.
4. In bots mode, `pr-loop end PR...` deletes the loop's trigger comments.
