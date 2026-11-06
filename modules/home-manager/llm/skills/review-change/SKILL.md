---
name: review-change
description: Select reviews by the change's risks, verify fixes within one review allowance, and triage review feedback. Use before starting a change, before handing one back, and when review feedback needs an outcome or reply.
---

# Reviewing a change

Every session that commits a change runs this pipeline, unless the operator
opted out of it for the task. Quality does not depend on whether the work
was split among sub-agents: a coordinator runs the same pipeline, and the
`coordinate-subagents` skill adds only what several branches need.

## The pipeline

1. **Plan.** Before coding, write the plan as the `plan-work` skill says:
   the goal, what is not this change's job, and the items the change is made
   of, each with an ID. Record every review outcome against the plan item ID
   it concerns, as "Recording it in the log" below says.
2. **Select reviews.** Use Review selection below; log the selected reviews
   and the concrete question each must answer. Run a design review before
   coding when that selection calls for one.
3. **The work**, committed as the `signing-pass` skill says.
4. **Fresh review loop**, once the work is complete, for the reviews
   selected above. Settle each round's
   findings together, then verify the fixes as Fresh review loop says.
5. **Design review of the result** when the implementation materially
   changes the reviewed design or introduces a design risk not reviewed
   before coding.
6. **Publish and hand back.** Follow the `signing-pass` skill's "Handing
   over" and complete each agent-owned next step before ending the session.
   Report a refusal with its reason and the remaining step. The report
   includes what the work's own instructions ask for, and
   - each finding, its outcome and its plan item ID, round by round;
   - findings still standing, with the reviewer's evidence;
   - the design reviews' open questions for the operator and the decisions you
     made yourself, as one list;
   - the `retro` skill's proposals for findings settled `fix` and session
     papercuts, referring to those already recorded as they occurred.

## Review selection

Judge the current change's behaviour and affected callers, not its file
count. Ordinary prose and mechanical formatting need their checks alone;
agent instructions, permissions and configuration count by the behaviour
they control. Other changes get one fresh general reviewer, preferably
from a model family other than the author's. An additional general opinion
needs a specific unresolved question or the operator's request.

Select focused reviews for concrete questions the general review and
checks do not already answer:

- `security`: a changed trust boundary, authorization, secret handling,
  execution of untrusted input, or exposure through permissions or a sandbox.
- `compat`: changed persisted data or an interface another version, host
  or program consumes, including rollback across versions.
- `upgrade`: changed dependency versions and their effects on this code.
  Add security only for a distinct security question.
- Design review: a new architecture, ownership or state model, a
  significant interface decision, or interactions across parts whose
  structure needs judgement.

The scan and general reviewer return candidates, not dispatch decisions.
For each candidate, record the affected behaviour and either the question
for a specialist, the evidence already answering it, or why it does not
apply. The scan includes commits back to the default branch: a signal from
an unchanged parent or a reverted edit needs assessment against the current
change. File-writing calls in test fixtures, a lock-file path, or editing
agent prose alone do not establish an uncovered security risk. If the
impact cannot be bounded, select the relevant review and state the uncertainty.

Put a selected specialist's question and affected paths in `--items`, with
the goal and range. A reviewer may inspect callers and dependencies beyond
those paths to answer it. Generate a walkthrough with `--reviewer guide`
only when the operator requests one, on the tip they will read.

## The goal statement

The reviewer's only view of intent: the plan's Goal, or a part's, holding
the goal and what is not this change's job, with nothing of how the work
went. `agent-plan goal PLAN [--part P1]` prints it, and `git delegate`
reads it with `--plan PLAN [--part P1]`. When the caller names a plan and
part for the review, use their Goal as it stands.

Measure every proposed change against the goal, not against its size: a
large change the goal needs is in scope, and a one-line improvement it does
not need is not. Be skeptical of anything a reviewer or bot suggests that
the goal did not ask for, including refactors, hardening, extra tests and
fixes to code the change did not touch, and settle those `defer` with a
short reason. A deferral costs nothing; scope creep costs a reviewable
change.

## Recording it in the log

Each stage is an event in the plan's log (the `plan-work` skill's
`agent-plan log`), against the item it concerns: the plan's ID, a part's,
or a step's.

- Design review of the plan: `plan-reviewed`; each decision it brings is
  a question written with `agent-plan ask` and settled with
  `agent-plan decide`, which logs `decided`.
- Review selection: `planned`, with selected reviews, questions and the
  reasons for leaving candidates out.
- Before each fresh review round: `started` on the same plan review item,
  with a summary beginning `review round N/3`, followed by the selected
  reviewers and exact range. After it: `reviewed` with the same summary
  prefix and the findings count. The allowance counts those `started`
  entries, including any interrupted round.
- Each finding settled: `finding-settled` against the item it concerns, with
  `--review <round>.<id> --outcome <outcome>`, the outcome as Triage below.
- Design review of the result: `reviewed` with `--role design-reviewer`.
- Hand-back: `handed-back`, with `STATUS` set to `handed-back`.
- When the operator opts the task out, `PIPELINE` becomes `opted-out:<reason>`.

## Fresh review loop

Use one stable plan item for the change's review allowance. Read its log
before dispatching: at most three logical rounds, including fix
verification, across changed tips, rebases, resumes and provider fallbacks.
Moving the work to another item carries its used allowance with it. Each
round includes its general and selected specialist reviews; delegate's
per-reviewer record rounds are not this counter. Log the round before its
first dispatch so an interrupted run cannot silently restart it. Resume
only unfinished reviews in that round, with at most one provider fallback
for each; report a review still unavailable. At the limit, hand back open
findings and missing coverage; more rounds require the operator's decision.

The first round covers the whole change, `BASE..TIP`, with BASE where it
starts, such as `origin/main` or the parent branch in a stack.

1. Write the items the change answers (threads, CI checks), with how each
   was settled, to a file, if any. The goal statement is the plan's Goal,
   or the part's.
2. The general review: prefer `git delegate`, which selects an independent
   reviewer from the other family where available. Use `/code-review`
   against BASE in Claude Code, `/review` against BASE in Codex, or a fresh
   read-only `adversarial-reviewer` given the goal and range as alternatives,
   not additional passes. Where the repository has test settings, run
   `git -C WORKTREE red-check BASE..TIP > WORKTREE/red-check.json`, WORKTREE
   being the branch's worktree, not `$TMPDIR`, which a sandboxed command and
   git delegate resolve to different directories; every file you pass
   delegate goes in WORKTREE the same way, by its absolute path. Then run
   `git delegate --plan PLAN [--part P1] [--items ITEMS] [--red-check WORKTREE/red-check.json] BASE..TIP`
   as a command of its own, with no `cd` before it: it resolves the range
   from any worktree of the repository. Remove `red-check.json` after it.
   It runs one general review, as Harness choice says, reports focused
   review candidates, and keeps findings in the range's review record.
   Select focused reviews as Review selection says. It exits 0 with
   no open findings, 3 with open findings, 4 when a reviewer could not run
   here, and 5 when the record fails its structure check. On 4, run each
   review in `notRun` as Harness choice says; on 4 or 5, say so in the
   hand-back, with delegate's reason.
3. Settle every finding of both as Triage says. A delegate finding is
   settled in its record too: `git delegate settle BASE..TIP FINDING
   OUTCOME [--target X] [--reason-file F]`, by the record ID its output
   gives (`finding`, such as F3). `dropped` lists what pointed at no line
   at TIP or did not fit the schema; read it, but it needs no outcome.
   `routes` lists focused review candidates, from a scan of
   every commit since the default branch and from the reviewer.
   `git delegate show BASE..TIP` prints the whole record.
4. After fixes, run the relevant checks and verify the delta from the last
   reviewed tip to the new tip. Pass the original goal, relevant findings,
   their dispositions and the earlier coverage as `--items`; a new range
   starts its own record. Select only reviewers whose earlier conclusions
   the fix could invalidate, considering affected callers, shared contracts
   and dependencies as well as edited files. A reviewer reads every hunk
   of this delta and the context needed to judge it. Use the whole change
   again when its impact cannot be bounded, within the same allowance.
   A changed SHA alone does not invalidate completed review coverage.
5. Stop when in-scope findings are settled, fixes are verified and selected
   coverage is complete, or the allowance is spent. A rebuttal or deferral
   alone needs no new review. Reopen a settled finding only with new
   evidence. Report unresolved findings and unavailable checks explicitly;
   they are not a clean result.

## Focused reviews

Run a selected focused review with
`git delegate --reviewer ROUTE --plan PLAN --items ITEMS BASE..TIP`.
`security` reviews what the change lets someone do;
`compat` reviews what outlives the commit (stored state, flags, output
others parse, config keys and options, what another host or branch reads),
and checks the pull request description's `## Reverting` section against
the code, so it also takes `--description FILE`, the description as the
`pull-requests` skill has you write it; for a pull request it reads the
pull request's own unless `--description` names another. `upgrade`
reviews what the change's version bumps (lock files, Nix pins, CI
actions) change here, against upstream's commits, changelogs and
releases between each pin's two versions, which delegate gathers on the
host before the reviewer starts; the reviewer never fetches. An `UP-`
finding names a pin whose upstream changes nobody read; read them or say
in the reply why the pin is safe. Each runs headless as its route's own
agent type, `ROUTE-reviewer` (`security-reviewer`, `compat-reviewer`,
`upgrade-reviewer`); where delegate cannot run it (exit 4),
`git delegate --print-prompt --reviewer ROUTE` with the same files prints
the prompt for a new sub-agent of that type, which stderr names. Settle
each finding as Triage says. Verify fixes under the same selection and
allowance as Fresh review loop.

## Someone else's pull request

`git delegate OWNER/NAME#N` (or N for origin's), run from a clone of that
repository, fetches the pull request into the clone as
`refs/pr-review/N/head` and `refs/pr-review/N/base`, so the clone's agent
settings apply; run anywhere else, it fetches into a clone of its own under
`$XDG_CACHE_HOME/pr-review/OWNER/NAME/N/`, with none. It reviews
`merge-base..head` with the same reviewers, without running its
code; it refreshes the pull request's threads too. Run it in the
background: a run takes minutes. Wait for it to finish before you stop or
report. Then the operator reads its record in review-guide, and comments
in pr-review.

1. The plan's goal is ours, never the pull request's text: "review
   OWNER/NAME#N at its head for the operator; propose comments only".
   delegate writes the reviewer's goal statement itself; `--plan PLAN`
   adds ours.
2. For its tests, run red-check in a checkout of this clone:
   `git red-check $(git merge-base refs/pr-review/N/base refs/pr-review/N/head)..refs/pr-review/N/head
   > red-check.json` where delegate fetched into this clone. Where it used a
   clone of its own, fetch from it first, a local read the sandbox allows:
   `git fetch $XDG_CACHE_HOME/pr-review/OWNER/NAME/N/clone refs/pr-review/head
   refs/pr-review/base`, then `git red-check MERGE_BASE..FETCH_HEAD >
   red-check.json` in the working directory. Use that evidence to settle
   the findings. If it leaves a concrete review question unanswered,
   select a targeted follow-up within the same review allowance and pass
   the evidence with `--red-check red-check.json`; delegate refuses JSON
   of any other range.
3. Triage only proposes. Each finding's outcome is `propose`, `rebut`,
   `duplicate-of` (with `--target`) or `needs-operator`: never fix, push,
   reply or resolve on someone else's pull request. Write a proposed
   finding's reason as the comment the operator would post.
4. Record each outcome with `git delegate settle OWNER/NAME#N FINDING
   OUTCOME [--target X] [--reason-file F]`. The pull request's review
   record, under `$XDG_STATE_HOME/agent-tools/reviews/OWNER/NAME/`, is the
   one record of them, every round's, and `git delegate show
   OWNER/NAME#N` prints it with the threads. The plan's log gets only the
   round and the counts: `agent-plan log PLAN --item ID --kind reviewed
   --summary 'round 1: 5 findings, 3 proposed'`.
5. A review of the same head adds a round to its record; a new head is a
   new record, whose reviewers are given the last one's findings.

## Harness choice

- The general review, through `git delegate`: prefer a model family other
  than the author's. For a Claude author it is Codex, whose commands can read
  only the worktree under review, the git objects behind it and the system
  files they need, with none of the operator's Codex configuration, no
  network and no writes, so a change cannot steer it into reading
  credentials. For a Codex author (`CODEX_SANDBOX` or `CODEX_THREAD_ID`
  set) it is Claude Code's `adversarial-reviewer`, given the same contract
  and confined as the focused reviews are, and Codex, run fresh, where
  Claude cannot run; disclose that same-family fallback in the hand-back.
  `git delegate ask` picks the other family the same
  way.
- The focused reviews and the walkthrough, through `git delegate`: Claude
  Code's reviewer agents, run headless with no settings, MCP server or
  CLAUDE.md of anyone's, in a sandbox of their own.
- A provider the operator says is exhausted or must not be called: run
  `git delegate availability --disable claude` (or `codex`) before
  reviewing. `git delegate availability` shows each provider's hold and
  calls neither. A failed call records its own hold: a rate limit (a 429)
  holds its provider for five minutes; an exhausted quota holds it until
  its reset where Claude's quota windows show one, and otherwise until the
  operator reports recovery and you run
  `git delegate availability --clear claude` (or `codex`).
- Each review in `notRun` (exit 4): run `git delegate --print-prompt
  --reviewer TYPE` with the same plan, range and files, and give what it
  prints to a new read-only sub-agent of the agent type stderr names, with
  none of your own reasoning; for `git delegate ask`, add `--print-prompt`
  to the same command. A diagram's prompt holds a drawing task and a
  critique task, each for a sub-agent of its own. Name each such review,
  and the harness it ran in, in the hand-back.

## Design review

Run when Review selection or the pipeline's result check calls for it.
For a template's unused design stage, log `abandoned` with the selection
reason. Start a new sub-agent that only reads (Claude Code's
`design-reviewer` agent; elsewhere one told to only read), and give it
the plan, the goal statements and, for the result, every branch with its
range. It reviews the work as a whole, where the fresh review loop reviews
each branch:

- structure: what goes where, what is shared, which branches stack;
- duplication of, or contradiction with, what already exists, documentation
  and tooling included, searched for by what it does rather than by name,
  and saying where it looked;
- the same file edited on parallel branches;
- for routing configuration, trace every configured provider and two
  generations of delegation; identify which context owns inherited defaults;
- a new or changed check: where the repository documents how to check a
  change, its entry names each path and pin that can break it;
- a program or function whose result is a decision: an error has a result
  of its own, never one of the decision's, and every caller fails on it;
- a changed mutation guard: tests compare the resources it authorizes with
  what the real command or filesystem changes, through alternate spellings
  and each input form; its suggested recovery command is permitted;
- what the plan leaves undecided, against this bar: someone could build it
  without asking a question;
- designing for change, by each principle and check the `design` skill
  lists (tell the sub-agent to load the `design` skill and its
  principles), and each of the goal's likely next changes a local edit;
- as judgement calls, never as rules: data clumps, primitive obsession, a
  middle man, the same switch in several places, feature envy, and one
  module changed for unrelated reasons.

It may read earlier plans in the same area (`agent-plan find`, or the
plans directory) to see what kept changing there.

While the plan leaves an interface undecided, load the
`design-it-several-times` skill before choosing one.

Where you and the design review disagree, or the designs leave no clear
choice, get a second opinion from another model family before it is
decided: write the question and its options to a file, and run
`git delegate ask --question FILE PATH...`, naming each file in the
repository the answer rests on (put what it needs from the plan into the
question file, under the working directory). The selected reviewer reads
those files and nothing else. On exit 4, ask a new sub-agent that only
reads, as Harness choice says. The answer goes
beside your recommendation; it informs your own decision, or the operator's
on a one-way door, and settles nothing.

It reports in the fresh review's vocabulary where that fits. Settle what
the goal statement already decides by Triage. Take the rest through the
`plan-work` skill's "Questions for the operator", which says which are theirs to
answer and which are yours to decide; those that change the plan are
settled before the work starts.

## Diagrams

A diagram shows the code a change touches, drawn by one model family and
critiqued by the other. Draw one on request alone: where the operator asks
for it; where the design review does, for a change that crosses two or more
packages or modules, or adds a state set or a cross-process protocol; or
where a pull request's `## Visuals` needs one, as the `pull-requests`
skill's description says.

Add `--reviewer diagram` to the review that needs it, as
`git delegate --reviewer codex --reviewer diagram --plan PLAN BASE..TIP`.
Claude Code's `diagrammer` agent draws at most three diagrams, each element
citing the lines at TIP it stands for; delegate holds every citation to the
code, Codex critiques each element by element, and one the critic faults is
redrawn once. Those that hold are kept in the review record, where review-guide
and `git delegate show` render them; the rest are under the round's Dropped.
delegate posts nothing to GitHub; a kept diagram reaches a pull request only
through its description's `## Visuals`.

## Triage

The one definition of review outcomes, for bot threads, people's threads
and reviewers' findings alike. Who posts the answer depends on the thread:
see Answering. Check each item against the code before judging it. When a
finding returns after its fix, load the `attack-the-premise` skill before
fixing it again.

- **fix**: real and inside the goal statement. A fixup of the commit that
  owns the code. Answer with what changed, and resolve once the fix is
  pushed.
- **propose**: what would be fix, on someone else's pull request. Nothing
  is changed or posted: the finding's `body` is the comment as the
  operator would post it.
- **rebut**: the reviewer is wrong. Show it from the code or a test, not by
  assertion. Answer with the evidence, and resolve.
- **duplicate-of `<item>`**: the same problem raised elsewhere, fixed once.
  Answer pointing at that item, and resolve once its fix is pushed.
- **belongs-in `<branch>`**, in a stack: code another branch introduced,
  fixed there. Answer naming where, and resolve once that fix is pushed.
- **defer**: real, but outside the goal statement. Answer with why, and that
  it awaits the operator; the thread stays open.
- **needs-operator**: a product or design call, another team's code, or
  anything the goal statement does not settle. Answer that it awaits
  the operator's decision; the thread stays open, and the item goes in the
  hand-back with its options.

Each finding settled `defer` or `belongs-in` is also written into the
plan with `agent-plan followup PLAN --title '...' --found-in <item>`, the
plan item it was found against, so the work stays in view after the
thread.

A bot whose `resolvesOwnThreads` is set resolves its own threads when it
reviews again; leave those to it. A reviewer's finding has no thread: its
outcome goes against its plan item ID and in the hand-back.

## Answering

- Every fix is a fixup carrying trailers that name what it answers, in a
  review loop or not; the squash drops them with the fixup's message. They
  are the only trailers a commit carries.

  ```sh
  git -c commit.gpgsign=false commit --fixup=<commit> \
    --trailer "Review-Thread: <thread URL>" \
    --trailer "Review-Bot: <bot>" \
    --trailer "Review-Round: <round>"
  ```

  For a CI failure, `Review-Check: <check>` replaces the thread and bot
  trailers. For a reviewer's finding, `Review-Finding: <round>.<id>`
  does, with the fresh review loop's round and the finding's `id`.
- A reply names commits by subject, never by SHA: the signing pass rewrites
  every SHA.
- **A review bot's thread:** draft the replies in the format `pr-loop
  reply --from` reads, one file per pull request (`pr-loop --help`): a
  `=== thread THREAD` line, or `=== thread THREAD resolve` where the
  outcome resolves, then the reply. In a bots-mode loop (`review-loop
  --bots`), post them with `pr-loop reply --from FILE` once the fixes are
  pushed; pr-loop takes only threads whose every comment is a configured
  bot's or the operator's. Outside one, the drafted replies go in the
  hand-back, as a person's thread's do.
- **After a round of bot fixes, in a loop or not:** once the fixes are
  pushed and the replies posted, ask each bot that had threads to review
  again, on every pull request whose head moved, without being asked. In a
  bots-mode loop, the `review-loop` skill's round does this. Outside one,
  `pr-loop request` refuses, so the hand-back gives the operator one line,
  to run once they have posted the replies: `git sign-pass --push`, naming
  the top of each stack as the `signing-pass` skill says, and a `gh pr edit
  N --add-reviewer @copilot` per pull request, all joined with `&&`. `gh pr
  edit` prints only the URL, and requested_reviewers stays empty for a bot;
  the review_requested event in the issue timeline confirms the request.
- **Then wait for the reviews, outside a bots-mode loop,** where the
  `review-loop` skill's round waits instead: once the sign-pass result shows
  the push, or the operator says the request ran, run `pr-loop status`;
  while a bot asked again is neither pending nor has reviewed the new head,
  its request has not run, so tell the operator. Then start `pr-loop wait
  reviews --timeout 3600 PR...` in the background, as a command of its
  own, and start it again while it exits 3, at most three times, then tell
  the operator; reviews take 5 to 15 minutes. On any other exit, tell the operator what it printed. When it
  exits 0, run `pr-loop threads` on each pull request and settle the new
  threads as Triage says, without waiting to be asked. Stop when `pr-loop
  status` shows each bot asked again has reviewed the new head and its
  review left no new thread; or after six rounds, handing back what is
  still open.
- **A person's thread,** in a people-mode loop (`review-loop
  --people`): write each reply with `pr-loop draft THREAD --body-file F
  --outcome OUTCOME`, and commit the fixups. Push nothing and post
  nothing: the operator reads the fixups and drafts, pushes with `git
  sign-pass --push`, and posts with `pr-loop post` from a terminal.
  Outside people mode, a person's thread goes in the hand-back with its
  proposed answer.
