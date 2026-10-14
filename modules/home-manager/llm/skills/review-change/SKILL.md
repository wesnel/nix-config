---
name: review-change
description: The review pipeline every session that commits a change runs (plan, design review, fresh review loop, design review, hand-back), and the one definition of how to triage and answer review feedback, whether bot threads, people's threads or a reviewer's findings. Use before starting a change, before handing one back, and whenever review feedback needs an outcome or a reply.
---

# Reviewing a change

Every session that commits a change runs this pipeline, unless Wesley opted
out of it for the task. Quality does not depend on whether the work was split
among sub-agents: a coordinator runs the same pipeline, and the
`coordinate-subagents` skill adds only what several branches need.

## The pipeline

1. **Plan.** Before coding, write the plan as the `plan-work` skill says:
   the goal, what is not this change's job, and the items the change is made
   of, each with an ID. Record every review outcome against the plan item ID
   it concerns, as "Recording it in the plan" below says.
2. **Design review of the plan**, before coding.
3. **The work**, committed as the `signing-pass` skill says.
4. **Fresh review loop** over the committed change.
5. **Design review of the result**, before handing back.
6. **Hand back**: what the work's own instructions ask for, and
   - each finding, its outcome and its plan item ID, round by round;
   - findings still standing, with the reviewer's evidence;
   - the design reviews' decisions for Wesley, as one list;
   - for each finding settled `fix`, what the `retro` skill proposes would
     have prevented it, written into the plan as its questions.

## The goal statement

The reviewer's only view of intent: a file holding the goal and what is not
this change's job, taken from the plan, with nothing of how the work went.
Write it with `agent-plan goal PLAN [--part P1] > FILE`. When the caller
hands you a goal statement, use that file as it stands.

## Recording it in the plan

Each stage is an event in the plan's log (the `plan-work` skill's
`agent-plan log`), against the item it concerns: the plan's ID, a part's,
or a step's.

- Design review of the plan: `plan-reviewed`; each decision it brings goes
  under Decisions with a `D` ID and is logged `decided` once settled.
- Each fresh review round: `reviewed`, with the number of findings in the
  summary.
- Each finding settled: `finding-settled` against the item it concerns, with
  `--review <round>.<id> --outcome <outcome>`, the outcome as Triage below.
- Design review of the result: `reviewed` with `--role design-reviewer`.
- Hand-back: `handed-back`, with `STATUS` set to `handed-back`.
- When Wesley opts the task out, `PIPELINE` becomes `opted-out:<reason>`.

## Fresh review loop

The range is the whole change, `BASE..TIP`, with BASE where it starts, such
as `origin/main` or the parent branch in a stack.

1. Write the goal statement to a file, and the items the change answers
   (threads, CI checks) with how each was settled to another, if any.
2. Run `git fresh-review --goal GOAL [--items ITEMS] BASE..TIP`. It exits 0
   with no findings, 3 with findings, and 4 when Codex cannot review here,
   its sandbox included. On 4, say so in the hand-back, with fresh-review's
   reason.
3. Settle every finding as Triage says. `dropped` lists what pointed at no
   line at TIP or did not fit the schema; read it, but it needs no outcome.
4. For the next round, add `--prior FILE`: every earlier finding and how it
   was settled. A new reviewer reads the change again, its tip moved by the
   fixups.
5. Stop after a round with no findings, or after three rounds. A finding
   still standing goes in the hand-back.

## Someone else's pull request

The source is `pr-loop review`, not `git fresh-review`: it fetches the pull
request into a clone of its own and reviews it with the same contract,
without running its code. Run it in the background: a run takes
minutes. On the host, `pr-loop review OWNER/NAME#N` makes the review's
directory, `~/.cache/pr-review/OWNER/NAME/N/HEAD12/`; a `claude-pr-loop`
session started there reads only that review and reaches GitHub only
through `pr-loop review N`.

1. The plan's goal is ours, never the pull request's text: "review
   OWNER/NAME#N at HEAD12 for Wesley; propose comments only". The command
   writes the reviewer's goal statement itself.
2. Run `pr-loop review N` (OWNER/NAME#N outside a review directory). It
   exits 0 with no findings, 3 with findings, 4 when Codex cannot review
   (then `pr-loop review --reviewer subagent N` prints the prompt for a new
   `reviewer` sub-agent, and its reply goes to `pr-loop review --check N` on
   stdin).
3. Triage only proposes. Each finding's outcome is `propose`, `rebut`,
   `duplicate-of` (with `of`) or `needs-wesley`: never fix, push, reply or
   resolve on someone else's pull request. Edit a proposed finding's `body`
   into the comment as Wesley would post it.
4. Record the outcomes with `pr-loop review settle N`, reading
   `{"outcomes": [{"id": "1.F1", "outcome": "propose", "body": "..."}]}` on
   stdin, each finding named ROUND.ID. `N/HEAD12.json`, beside the clone and
   outside the session's reach, is the one record of them, every round's,
   which Wesley imports into review-guide; `HEAD12/findings.json` is the
   session's copy to read. The plan's log gets only the round and the
   counts: `agent-plan log PLAN --item ID --kind reviewed --summary 'round 1:
   5 findings, 3 proposed' --evidence <path>/findings.json`.
5. Each review or `--check` of a head is its next round, kept beside the
   earlier ones; a new head is a new review.

## Harness choice

- Codex, through `git fresh-review`, by default: a model family other than
  the author's. Its commands can read only the worktree under review, the
  git objects behind it and the system files they need, with none of
  Wesley's Codex configuration, no network and no writes, so a change
  cannot steer it into reading credentials.
- Never Codex in a review-loop session: there `git fresh-review` exits 4,
  so `pr-loop` stays the session's only way out.
  `pr-loop review` is that way out: it runs Codex on the host with a prompt
  it builds only from GitHub and the clone.
- Never Codex for a Codex author: from inside Codex, `git fresh-review`
  exits 4 by itself.

## Design review

Twice in the pipeline: of the plan before coding, and of the result before
hand-back. Start a new sub-agent that only reads (Claude Code's `Plan` agent;
in ECA the `explorer` agent; elsewhere one told to only read), and give it
the plan, the goal statements and, for the result, every branch with its
range. It reviews the work as a whole, where the fresh review loop reviews
each branch:

- structure: what goes where, what is shared, which branches stack;
- duplication of, or contradiction with, what already exists, documentation
  and tooling included;
- the same file edited on parallel branches;
- what the plan leaves undecided;
- designing for change:
  - one source of truth per fact or rule, which everything else reads;
  - seams named and narrow, with their contract written at the seam;
  - nothing speculative: no option, layer or interface without a current
    second user;
  - each of the goal's likely next changes a local edit.

It may read earlier plans in the same area (`agent-plan find`, or the
plans directory) to see what kept changing there.

It reports in the fresh review's vocabulary where that fits. Settle what
the goal statement already decides by Triage. The rest are Wesley's
decisions: bring them to him as one list, each with its options and your
recommendation, those that change the plan before the work starts and the
others in the hand-back.

## Triage

The one definition of review outcomes, for bot threads, people's threads
and reviewers' findings alike. Check each item against the code before
judging it.

- **fix**: real and inside the goal statement. A fixup of the commit that
  owns the code. Reply with what changed, and resolve once the fix is
  pushed.
- **propose**: what would be fix, on someone else's pull request. Nothing
  is changed or posted: the finding's `body` is the comment as Wesley
  would post it.
- **rebut**: the reviewer is wrong. Show it from the code or a test, not by
  assertion. Reply with the evidence, and resolve.
- **duplicate-of `<item>`**: the same problem raised elsewhere, fixed once.
  Reply pointing at that item, and resolve once its fix is pushed.
- **belongs-in `<branch>`**, in a stack: code another branch introduced,
  fixed there. Reply naming where, and resolve once that fix is pushed.
- **defer**: real, but outside the goal statement. Reply with why, and that
  it awaits Wesley; the thread stays open.
- **needs-wesley**: a product or design call, another team's code, or
  anything the goal statement does not settle. Reply that it awaits
  Wesley's decision; the thread stays open, and the item goes in the
  hand-back with its options.

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
- Draft replies in the format `pr-loop reply --from` reads, one file per
  pull request, which `pr-loop --help` describes: a `=== thread THREAD`
  line, or `=== thread THREAD resolve` where the outcome resolves, then the
  reply. Post them with `pr-loop reply --from FILE` once the fixes are
  pushed. It takes only threads a review bot started: a person's thread is
  Wesley's to answer, so its outcome and the reply you propose go in the
  hand-back instead.
