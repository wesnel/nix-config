---
name: review-change
description: The review pipeline every session that commits a change runs (plan, design review, fresh review loop, design review, hand-back), and the one definition of how to triage and answer review feedback, whether bot threads, people's threads or a reviewer's findings. Use before starting a change, before handing one back, and whenever review feedback needs an outcome or a reply.
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
2. **Design review of the plan**, before coding.
3. **The work**, committed as the `signing-pass` skill says.
4. **Fresh review loop** over the committed change, once the work is
   complete: in Claude Code, `/code-review` for the general review; then
   `git delegate` as the second opinion. Settle each round's findings
   together before the next round.
5. **Design review of the result**, before handing back.
6. **Hand back**: what the work's own instructions ask for, and
   - each finding, its outcome and its plan item ID, round by round;
   - findings still standing, with the reviewer's evidence;
   - the design reviews' open questions for the operator and the decisions you
     made yourself, as one list;
   - for each finding settled `fix`, what the `retro` skill proposes would
     have prevented it, each a question written with `agent-plan ask`.

## The goal statement

The reviewer's only view of intent: a file holding the goal and what is not
this change's job, taken from the plan, with nothing of how the work went.
Write it with `agent-plan goal PLAN [--part P1] > FILE`. When the caller
hands you a goal statement, use that file as it stands.

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
- Each fresh review round: `reviewed`, with the number of findings in the
  summary.
- Each finding settled: `finding-settled` against the item it concerns, with
  `--review <round>.<id> --outcome <outcome>`, the outcome as Triage below.
- Design review of the result: `reviewed` with `--role design-reviewer`.
- Hand-back: `handed-back`, with `STATUS` set to `handed-back`.
- When the operator opts the task out, `PIPELINE` becomes `opted-out:<reason>`.

## Fresh review loop

The range is the whole change, `BASE..TIP`, with BASE where it starts, such
as `origin/main` or the parent branch in a stack.

1. Write the goal statement to a file, and the items the change answers
   (threads, CI checks) with how each was settled to another, if any.
2. In Claude Code, run `/code-review` with BASE..TIP as its target: with none, it
   reviews only the commits ahead of the branch's upstream, which a local
   branch may not have. For a stacked branch, give /code-review the range
   BASE..TIP; a bare branch name is reviewed from its merge base with the
   default branch. Then the second opinion, from another model
   family: where the repository has test settings, run
   `git -C WORKTREE red-check BASE..TIP > WORKTREE/red-check.json`, WORKTREE
   being the branch's worktree, not `$TMPDIR`, which a sandboxed command and
   git delegate resolve to different directories; every file you pass
   delegate goes in WORKTREE the same way, by its absolute path. Then run
   `git delegate --goal GOAL [--items ITEMS] [--red-check WORKTREE/red-check.json] BASE..TIP`
   as a command of its own, with no `cd` before it: it resolves the range
   from any worktree of the repository. Remove `red-check.json` after it.
   It runs Codex, and each focused review its scan routes the change to,
   and keeps their findings in the range's review record. It exits 0 with no open findings, 3 with open
   findings, 4 when a reviewer could not run here, and 5 when the record
   fails its structure check. On 4 or 5, say so in the hand-back, with
   delegate's reason.
3. Settle every finding of both as Triage says. A delegate finding is
   settled in its record too: `git delegate settle BASE..TIP FINDING
   OUTCOME [--target X] [--reason-file F]`, by the record ID its output
   gives (`finding`, such as F3). `dropped` lists what pointed at no line
   at TIP or did not fit the schema; read it, but it needs no outcome.
   `routes` lists the focused reviews the change needs, from a scan of
   every commit since the default branch and from the reviewer.
   `git delegate show BASE..TIP` prints the whole record.
4. For the next round, run `git delegate` on the range again: the record
   gives the new reviewers every earlier finding and how it was settled. A
   new range, its tip moved by the fixups, starts a record of its own; pass
   the earlier findings that still matter as `--items`.
5. Stop after a round with no findings, or after three rounds. A finding
   still standing goes in the hand-back.

## Focused reviews

Without `--reviewer`, delegate runs each focused review its scan routes
the change to. A route only the reviewer named is printed with the command
that runs it: `git delegate --reviewer ROUTE --goal GOAL BASE..TIP`, the
same range and goal. `security` reviews what the change lets someone do;
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
each finding as Triage says. A fix moves the change, so the general
review and each route's run again on it, within the same three rounds.

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
   delegate writes the reviewer's goal statement itself; `--goal` adds ours.
2. For its tests, run red-check in a checkout of this clone:
   `git red-check $(git merge-base refs/pr-review/N/base refs/pr-review/N/head)..refs/pr-review/N/head
   > red-check.json` where delegate fetched into this clone. Where it used a
   clone of its own, fetch from it first, a local read the sandbox allows:
   `git fetch $XDG_CACHE_HOME/pr-review/OWNER/NAME/N/clone refs/pr-review/head
   refs/pr-review/base`, then `git red-check MERGE_BASE..FETCH_HEAD >
   red-check.json` in the working directory. Then run
   `git delegate --red-check red-check.json OWNER/NAME#N`, which adds a
   round. delegate refuses red-check JSON of any other range.
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

- Codex, through `git delegate`, for the second opinion: a model family
  other than the author's. Its commands can read only the worktree under
  review, the git objects behind it and the system files they need, with
  none of the operator's Codex configuration, no network and no writes, so
  a change cannot steer it into reading credentials.
- The focused reviews, through `git delegate --reviewer TYPE`: Claude Code's
  reviewer agents, run headless with no settings, MCP server or CLAUDE.md
  of anyone's, in a sandbox of their own.
- Never Codex in a review-loop session, which reads other people's review
  text: there delegate's Codex review exits 4, and the second opinion is an
  `adversarial-reviewer` sub-agent.
- Never Codex for a Codex author: from inside Codex, `git delegate`
  without `--reviewer` exits 4 by itself.

## Design review

Twice in the pipeline: of the plan before coding, and of the result before
hand-back. Start a new sub-agent that only reads (Claude Code's
`design-reviewer` agent; elsewhere one told to only read), and give it
the plan, the goal statements and, for the result, every branch with its
range. It reviews the work as a whole, where the fresh review loop reviews
each branch:

- structure: what goes where, what is shared, which branches stack;
- duplication of, or contradiction with, what already exists, documentation
  and tooling included, searched for by what it does rather than by name,
  and saying where it looked;
- the same file edited on parallel branches;
- what the plan leaves undecided, against this bar: someone could build it
  without asking a question;
- designing for change, by each principle the `design` skill lists (tell
  the sub-agent to load them), and each of the goal's likely next changes
  a local edit;
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
question file, under the working directory). Codex reads those files and nothing else. On exit 4, ask a
new sub-agent that only reads instead, as the message says. The answer goes
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
for it, or the design review does, for a change that crosses two or more
packages or modules, or adds a state set or a cross-process protocol.

Add `--reviewer diagram` to the review that needs it, as
`git delegate --reviewer codex --reviewer diagram --goal GOAL BASE..TIP`.
Claude Code's `diagrammer` agent draws at most three diagrams, each element
citing the lines at TIP it stands for; delegate holds every citation to the
code, Codex critiques each element by element, and one the critic faults is
redrawn once. Those that hold are kept in the review record, where review-guide
and `git delegate show` render them; the rest are under the round's Dropped.
A diagram is never posted to GitHub: a pull request's description stays the
operator's.

## Triage

The one definition of review outcomes, for bot threads, people's threads
and reviewers' findings alike. Who posts the answer depends on the thread:
see Answering. Check each item against the code before judging it. When a
finding returns after its fix, load the `attack-the-premise` skill before
fixing it again.

- **fix**: real and inside the goal statement. A fixup of the commit that
  owns the code. Answer with what changed, and resolve once the fix is
  pushed. A fix that adds text to a doc or comment changes what the reader
  does; if the nearby text already makes the point, the fix is a rebut.
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
  outcome resolves, then the reply. In a bots-mode loop (`claude-review-loop
  --bots`), post them with `pr-loop reply --from FILE` once the fixes are
  pushed; pr-loop takes only threads whose every comment is a configured
  bot's or the operator's. Outside one, the drafted replies go in the
  hand-back, as a person's thread's do.
- **A person's thread,** in a people-mode loop (`claude-review-loop
  --people`): write each reply with `pr-loop draft THREAD --body-file F
  --outcome OUTCOME`, and commit the fixups. Push nothing and post
  nothing: the operator reads the fixups and drafts, pushes with `git
  sign-pass --push`, and posts with `pr-loop post` from a terminal.
  Outside people mode, a person's thread goes in the hand-back with its
  proposed answer.
