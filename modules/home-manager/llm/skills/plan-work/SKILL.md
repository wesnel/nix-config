---
name: plan-work
description: Keep a plan file and its event log for a change with agent-plan - where plans live, their format and item IDs, what to log and when, finding a plan again after a compaction, a resume or a hand-off, and planning without acting when Wesley asks only for a plan. Use before starting any change, when picking one up again, and whenever Wesley asks for a plan.
---

# Plans

Every change has a plan: files made by `agent-plan new` side by side in the
plans directory, `<date>-<slug>.<host>.org`, a
`<date>-<slug>.<host>.<part>.org` per sub-agent's part, and
`<date>-<slug>.<host>.log.org`. You and whoever picks the work up after you
work from it, so keep it true.

## Making one

```sh
agent-plan new --title 'Add the run loop' --slug add-run-loop \
  --repo owner/name --branch main..add-run-loop
```

It prints the plan's file (`plan`, the PLAN every other command takes), its
ID and its log. The worktree and, in Claude Code, the session are recorded
from where it runs; pass `--worktree` and `--session HARNESS:ID` otherwise.
For work that recurs, `--template NAME` starts the plan with the entries
of each `**` section of `templates/NAME.org` in the plans' repository;
Wesley keeps those files.

A sub-agent given part `P1` of a plan makes its own file beside the parent's
and logs to the same log:

```sh
agent-plan new --parent PLAN --part P1 --title 'Add the run loop flag' --branch add-run-loop..run-loop-flag
```

## The plan file

- `* Plan`, whose properties say where the work is: `ID`, `HOST`,
  `REPOSITORIES`, `BRANCHES` (`base..branch`), `WORKTREES`, `SESSIONS`
  (`harness:id`), `HARNESS`, `STATUS` (`planning`, `reviewing-plan`,
  `working`, `reviewing`, `handed-back` or `halted`), `PULL_REQUESTS`,
  `PIPELINE` (`full` or `opted-out:<reason>`), `SKILLS` (the skills the
  work runs under beyond this one, such as `coordinate-subagents` or
  `review-loop`, which you add) and, once `agent-plan own` sets one,
  `OWNERS` (`worktree=agent`). Lists are separated by spaces.
- `** Goal`: the goal and what is not this change's job, and nothing of how
  the work went. It is the fresh review's goal statement, word for word:
  `agent-plan goal PLAN [--part P1]` prints it. It ends with a
  `Likely next changes:` line: one or two concrete changes expected after
  this one, not speculation, which the design review of the plan checks
  would each be a local edit.
- `** Steps`: `*** TODO <step>` headings with `:ID: S1`, `S2`…; a part's
  are `P1.S1`, `P1.S2`… Each says what observable result marks it done,
  which its `verified` evidence then shows. Steps that must run in order
  go under a heading with `:ORDERED: t`, and a step that waits on another
  item lists it in `:AFTER:`; `agent-plan status` lists the steps ready
  now. A step or part that cannot go on until a time is postponed with
  `agent-plan postpone PLAN S1 --until '2026-10-20 18:30'`, which keeps it
  out of the ready lists and the agenda until then; `--clear` undoes it.
- `** Distribution`, in a plan split among sub-agents: a `*** TODO <part>`
  heading per part with `:ID: P1`…, its branch, base and worktree, a link
  to its file, and its steps.
- `** Questions`: every question for Wesley and every decision, each with
  `:ID: Q1`, `Q2`… (`P1.Q1` in a part's file), written by `agent-plan ask`
  as "Questions for Wesley" says, never by hand. An open one is `ASK`; a
  settled one stays where it is, `DECIDED` with its `:ANSWER:`, or
  `DROPPED`, or `SUPERSEDED` by the decision whose `:SUPERSEDES:` names
  it. `agent-plan decisions PLAN --all` lists the decisions. A suggestion
  rejected for a reason that will still hold, such as a constraint rather
  than a lack of time, is a decision too, with that reason, so a later
  design review finds it instead of proposing it again.
- `** Follow-ups`: work found while doing an item but outside this
  change's job, each with `:ID: F1`, `F2`… and the `:FOUND_IN:` item, as
  "Follow-ups" says.
- `** Verification`: how the change is shown to work: each seam the tests
  cross, and what each cannot catch.

The `#+TODO:` lines `agent-plan new` writes define the keywords; Emacs and
`agent-plan` both read them, so leave them as they are. The ID patterns
(`S`, `P`, `Q`, `F`, with an optional `P<n>.` prefix) are what Emacs's
plan views select entries by.

A question or a part that cannot start before others are settled lists
their IDs in `:AFTER:`, separated by spaces (a question's through
`agent-plan ask --after`): a question is settled once its keyword is a
done one, a part or step once `done`, `verified` or `abandoned` is logged
for it. Until then the question is not put to Wesley and the part is not
ready.

An ID is never reused or renumbered; a step dropped from the work keeps its
ID and gets an `abandoned` event. Edit only your own plan file, and change
it in the same step as what it describes: a new step, a decision, a moved
base.

## The log

The log is append-only, and only `agent-plan log` writes it; the guard
denies any other write, and a log changed some other way is refused from
then on.

```sh
agent-plan log PLAN --item S1 --kind started
agent-plan log PLAN --item S1 --kind done --commit 'Add the run loop' --evidence 'go test ./... passes'
agent-plan log PLAN --item S1 --kind verified --evidence 'red-check: fails-as-asserted'
```

- Log `started`, `done` and `verified` for every step. `verified` names
  evidence you observed, never one you expect.
- `log` writes a step's or part's heading keyword from its log
  (`STARTED`, `DONE`, `VERIFIED`, `ABANDONED`, with `CLOSED` as Org
  writes it); never change those keywords by hand. When it cannot, the
  event is still logged, its output says why, and `agent-plan status`
  lists the item under keyword drift.
- A hook after each shell call logs `committed` for every commit and
  `pushed` for every push, and adds new worktrees, branches and pull
  requests to the plan's properties, in the plan of the session or of the
  worktree it ran in; log neither yourself. Which step a commit completes
  stays yours to log.
- `--commit` takes the commit's subject: the signing pass changes every SHA.
- An event about the whole plan names the plan's ID; about a part, the
  part's.
- `--role` is yours: `single`, `parent` or `subagent`.
- Which model and effort to start a sub-agent with is the
  `coordinate-subagents` skill's "Model and effort".
- The review pipeline's events are the `review-change` skill's.

`agent-plan status PLAN` lists the steps without `done` and those done but
not `verified`, the parts ready to start, the steps ready now, what still
waits on an `:AFTER:` item, keyword drift, steps started with nothing
logged for a day, and commits no `done` event names (a `fixup!`, `amend!`
or `squash!` commit counts as the one it names). Log `done` with the
`--commit` of each listed commit that completes a step, or say why none
does.

## Finding it again

After a compaction, a resume or a hand-off, find the plan before anything
else, then read it, load the skills its `SKILLS` names, and run
`agent-plan status PLAN`:

- Claude Code names it when the session resumes or compacts.
- Elsewhere, `agent-plan find` lists the plans that match this session,
  worktree or branch; `--session`, `--worktree` or `--branch` matches the
  one given instead.
- A sub-agent's task names its plan and part.

After a compaction the hooks also add the plan's brief: its goal,
decisions, ready steps and parts, questions for Wesley, steps done but not
verified, and open follow-ups. `agent-plan brief PLAN` prints it any time.

Only this host's plans, the files named for it, are yours to change; another
host's beside them are read-only here, and `agent-plan` refuses to write
them.

## Questions for Wesley

Every question for Wesley lives in the plan's Questions, with your
recommendation, and nowhere else. Write each with `agent-plan ask`, which
gives it the next ID and the form `agent-plan questions` reads; a question
written by hand in another form never reaches Wesley:

```sh
agent-plan ask PLAN --question 'Keep the old flag?' --recommendation 'no, drop it' \
  --context 'Nothing reads it.' --alternative 'keep it' --after Q1 --priority A
```

`--part P1` puts it in that part's file; `--priority A` puts it before the
others, `C` after. Never restate open questions in a message: when a
sub-agent reports, say only what changed, add its new questions to the
plan, and at most give their number. When Wesley asks, print
`agent-plan questions PLAN --all`, the questions he can answer now, or
point him to the plan. When he answers, record each answer with
`agent-plan decide PLAN Q1 --answer 'ANSWER'`, which settles the question
in place and logs `decided`; `--drop --answer 'why'` drops it, and
`--supersedes Q2` marks the earlier decision this one replaces. A decision
can make waiting questions askable, so list them again until none are
left. In Claude Code, Wesley answers them through `/questions`; elsewhere,
print them and take his answers in the chat. He may also settle one in
Emacs, with a note that `agent-plan decisions` reads as the answer.

An open question is a decision only Wesley can make, and only his answer
settles it: never decide one yourself. A fact that a
command, a file, the docs or a sub-agent's lookup can establish is yours to
find; write what you found into the question it informs. Each question
holds one idea, worded so that "yes" accepts your recommendation, and the
most important comes first.

The one decision of your own a plan records is one a fact or constraint
that already holds forces, never a choice: write it with
`agent-plan ask PLAN --question ... --recommendation ... --answer 'ANSWER: the constraint' --role R`,
which writes it already decided, so every decision is in one place.

## Follow-ups

Work you find while doing an item that is not this change's job is
recorded, not done:

```sh
agent-plan followup PLAN --title 'Guard accepts --draft=false' --found-in S1 \
  --context 'The guard reads only whether the flag is present.'
```

It becomes a `TODO` under Follow-ups with the next `F` ID (`--part P1`
for a part's file), and its keyword then follows `agent-plan log` as a
step's does. `agent-plan status` lists those still open.

## Other hosts

Plans reach every host through the notes repository around the plans
directory: a watcher outside the session commits and pushes this host's
files a few seconds after one changes, and `find`, `status` and `new` pull
first. Never commit, push or pull there yourself; `agent-plan sync` is the
watcher's and `agent-plan reseal` is Wesley's, and the guard denies both. A
log refused as changed stays as it is: name it in your report.

## Spikes

When running code is the quickest way to answer a design question, write
the question with `agent-plan ask` first, then answer it with throwaway code
on a local branch `spike/<slug>`, never pushed and never merged. Put what
it showed and the branch's name into the question, for Wesley to decide.
The real change is then written fresh under the plan; the spike's code is
not carried into it.

## Claude Code's plan mode

A plan approved in plan mode is written into the session's plan's Goal,
Steps and Questions before the first edit.

## Plan and halt

If Wesley asks you to plan, write the plan and stop: no edits outside the
plan's files, and no commits. Log `planned` for the plan's ID, set
`STATUS` to `halted`, and report the plan's path with its open questions.

## The pipeline

The plan is the first step of the `review-change` skill's pipeline, which
runs the rest and records each stage here.
