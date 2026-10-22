---
name: plan-work
description: Keep a plan file and its event log for a change with agent-plan - where plans live, their format and item IDs, what to log and when, finding a plan again after a compaction, a resume or a hand-off, and planning without acting when the operator asks only for a plan. Use before starting any change, when picking one up again, and whenever the operator asks for a plan.
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
the operator keeps those files.

A sub-agent given part `P1` of a plan makes its own file beside the parent's
and logs to the same log:

```sh
agent-plan new --parent PLAN --part P1 --title 'Add the run loop flag' --branch add-run-loop..run-loop-flag
```

## The plan file

The plan file holds what is intended and decided now; the log holds what
happened. A branch built, reviewed, signed or pushed, a pull request
opened, a report accepted, a hand-back: each is a log event, never plan
text. Never write a dated section, a status or progress entry or a
hand-off section into a plan; `agent-plan brief PLAN` is the hand-off. A
plan file holds `* Plan` with the sections below; `agent-plan status`
lists any other heading, an entry
without its ID and a question without a question's keyword as structure
drift. Report any drift in your hand-back, and never ask to delete or
rewrite the operator's sections to silence it, nor log their history
afresh: the log records events as they happen.

Change a plan file only through `agent-plan`, never with a file-editing
tool or the shell: the guard refuses those writes, Claude Code's sandbox
denies the shell's writes to the plans directory, and every `agent-plan`
command refuses a change that would add structure drift and writes
nothing. Each section below names its command. Text (`--text`) may span
lines but holds no line Org reads as a heading, a drawer, a planning line
or a `#+` keyword other than a block's. A change no command makes goes in
your report for the operator.

- `* Plan`, whose properties say where the work is: `ID`, `HOST`,
  `REPOSITORIES`, `BRANCHES` (`base..branch`), `WORKTREES`, `SESSIONS`
  (`harness:id`), `HARNESS`, `STATUS` (`planning`, `reviewing-plan`,
  `working`, `reviewing`, `handed-back` or `halted`), `PULL_REQUESTS`,
  `PIPELINE` (`full` or `opted-out:<reason>`), `SKILLS` (the skills the
  work runs under beyond this one, such as `coordinate-subagents` or
  `review-loop`) and, once `agent-plan own` sets one,
  `OWNERS` (`worktree=agent`). Lists are separated by spaces.
  `agent-plan set PLAN NAME VALUE... [--part P1]` sets each of them but
  `ID`, `HOST`, `PIPELINE` and `OWNERS`, as `agent-plan set PLAN STATUS
  working` or `agent-plan set PLAN SKILLS coordinate-subagents`.
  `WORKTREES`, `BRANCHES`, `PULL_REQUESTS` and `OWNERS` follow the log, as
  step keywords do: `agent-plan set PLAN BRANCHES main..loop [--part P1]`
  logs the whole new value; `agent-plan status` lists a value the log does
  not give, and a References entry it does not hold, as property drift.
- `** Goal`: the goal and what is not this change's job, and nothing of how
  the work went. It is the fresh review's goal statement, word for word:
  `agent-plan goal PLAN [--part P1]` prints it. It ends with a
  `Likely next changes:` line: one or two concrete changes expected after
  this one, not speculation, which the design review of the plan checks
  would each be a local edit. It holds text, no headings. Write it whole
  with `agent-plan section PLAN Goal --text TEXT [--part P1]`. Load the
  `design` skill before writing the steps: its principles are what the
  design review checks them against.
- `** Steps`: `*** TODO <step>` headings with `:ID: S1`, `S2`…; a part's
  are `P1.S1`, `P1.S2`… Each says what observable result marks it done,
  which its `verified` evidence then shows. Add one with
  `agent-plan step PLAN --title 'Add the run loop' --text 'Done when ...'
  [--after S1]... [--part P1]`, which gives it the next ID; change its title
  or text with `agent-plan edit PLAN S1 [--title T] [--text TEXT]`. A step
  that waits on another item, including one that must run after an
  earlier step, lists it in `:AFTER:`: `--after` when adding it, or
  `agent-plan after PLAN S2 S1` (`--clear` for none) later. A heading with
  `:ORDERED: t` the operator wrote orders the steps under it too.
  `agent-plan status` lists the steps ready
  now. A step or part that cannot go on until a time is postponed with
  `agent-plan postpone PLAN S1 --until '2026-10-20 18:30'`, which keeps it
  out of the ready lists and the agenda until then; `--clear` undoes it.
- `** Distribution`, in a plan split among sub-agents: a `*** TODO <part>`
  heading per part with `:ID: P1`…, its repository, branch, base and
  worktree, and what it does. Add one with `agent-plan part PLAN --title T
  --repo nix-config --branch main..run-loop --worktree DIR --text TEXT
  [--after P1]...`, which gives it the next ID and starts its text with
  where it works, and change it with `agent-plan edit` and `after` as a
  step.
- `** Questions`: every question for the operator and every decision, each with
  `:ID: Q1`, `Q2`… (`P1.Q1` in a part's file), written by `agent-plan ask`
  as "Questions for the operator" says. An open one is `ASK`; a
  settled one stays where it is, `DECIDED` with its `:ANSWER:`, or
  `DROPPED`, or `SUPERSEDED` by the decision whose `:SUPERSEDES:` names
  it. A decision is always such a question, settled in place and never
  renumbered, moved or given an ID of another kind; a question reworded is
  a new question, the old one dropped with `agent-plan decide --drop`.
  `agent-plan decisions PLAN --all` lists the decisions. A suggestion
  rejected for a reason that will still hold, such as a constraint rather
  than a lack of time, is a decision too, with that reason, so a later
  design review finds it instead of proposing it again.
- `** Follow-ups`: work found while doing an item but outside this
  change's job, each with `:ID: F1`, `F2`… and the `:FOUND_IN:` item, as
  "Follow-ups" says.
- `** References`: each URL the operator gave the work, one entry each with
  `:URL:`, and a note of what it was used for and which decisions (Q IDs)
  it fed. In Claude Code and Codex every URL in their prompts is added
  with no note; add one they give otherwise with
  `agent-plan reference PLAN URL`. When you
  are done with a reference, write its note:
  `agent-plan reference PLAN URL --note 'The pstack principles; fed Q3, Q4.'`
- `** Verification`: how the change is shown to work: each seam the tests
  cross, and what each cannot catch. It holds text, no headings: write it
  with `agent-plan section PLAN Verification --text TEXT [--part P1]`.

The `#+TODO:` lines `agent-plan new` writes define the keywords; Emacs and
`agent-plan` both read them. The ID patterns
(`S`, `P`, `Q`, `F`, with an optional `P<n>.` prefix) are what Emacs's
plan views select entries by.

A question or a part that cannot start before others are settled lists
their IDs in `:AFTER:`, separated by spaces (through `agent-plan ask
--after`, `step --after` or `part --after`, and `agent-plan after` once
written): a question is settled once its keyword is a
done one, a part or step once `done`, `verified` or `abandoned` is logged
for it. Until then the question is not put to the operator and the part is not
ready.

An ID is never reused or renumbered; a step dropped from the work keeps its
ID and gets an `abandoned` event. Change only your own plan file, in the
same step as what it describes: a new step (through `agent-plan step`), a
decision (`agent-plan ask` or `decide`), a moved base (`agent-plan set`).

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
  worktree it ran in; `agent-plan log` refuses both. A commit no plan takes
  makes a plan for it, "Unplanned work in" its repository, which names the
  session and worktree; give it a goal and steps, or say in your report
  which plan the work belongs to. Which step a commit completes stays
  yours to log.
- `--commit` takes the commit's subject: the signing pass changes every SHA.
- An event about the whole plan names the plan's ID; about a part, the
  part's.
- `--role` is yours: `single`, `parent` or `subagent`.
- Which model and effort to start a sub-agent with is the
  `coordinate-subagents` skill's "Model and effort".
- A hand-back that leaves the operator a command to run names it:
  `agent-plan log PLAN --item P1 --kind handed-back --command 'git
  sign-pass --push' --dir WORKTREE`. The item waits on them, shown in
  `agent-plan waiting PLAN`, Emacs's agenda and the dashboard, until an
  event about its work, not its plan text or schedule, is logged for it. A
  sandbox cannot name one.
- The review pipeline's events are the `review-change` skill's.

`agent-plan status PLAN` lists the steps without `done` and those done but
not `verified`, the parts ready to start, the steps ready now, what still
waits on an `:AFTER:` item, keyword, property and structure drift, steps
started with nothing logged for a day, and commits no `done` event names
(a `fixup!`, `amend!` or `squash!` commit counts as the one it names). Log
`done` with the `--commit` of each listed commit that completes a step, or
say why none does.

## Finding it again

After a compaction, a /clear, a resume or a hand-off, find the plan before
anything else, then read it, load the skills its `SKILLS` names, and run
`agent-plan status PLAN`:

- Claude Code names it when the session resumes, compacts or is cleared.
- Elsewhere, `agent-plan find` lists the plans that match this session,
  worktree or branch; `--session`, `--worktree` or `--branch` matches the
  one given instead.
- A sub-agent's task names its plan and part.

After a compaction the hooks also add the plan's brief: its goal,
decisions, ready steps and parts, questions for the operator, steps done but not
verified, open follow-ups, and a line on any structure drift, which "The
plan file" says what to do about. `agent-plan brief PLAN` prints it any
time.
Claude Code adds `agent-plan brief PLAN --resume` after a compaction or
/clear instead: it spells out only the decisions since the last hand-back or
sign-pass and names the rest by ID, so the plan file holds the detail.
Log each hand-back and sign-pass when it happens: they are the checkpoints
the brief counts from, and where the status line suggests /clear to the
operator when the context is large or its cache cold.

Only this host's plans, the files named for it, are yours to change; another
host's beside them are read-only here, and `agent-plan` refuses to write
them.

## Questions for the operator

Every question for the operator lives in the plan's Questions, with your
recommendation, and nowhere else. Write each with `agent-plan ask`, which
gives it the next ID and the form `agent-plan questions` reads:

```sh
agent-plan ask PLAN --question 'Keep the old flag?' --recommendation 'no, drop it' \
  --context 'Nothing reads it.' --alternative 'keep it' --after Q1 --priority A
```

`--part P1` puts it in that part's file; `--priority A` puts it before the
others, `C` after. Never restate open questions in a message: when a
sub-agent reports, say only what changed, add its new questions with
`agent-plan ask`, and at most give their number. When the operator asks for
them in Claude Code, take `agent-plan questions PLAN --all --json`, the
questions they can answer now, and ask them through AskUserQuestion as
`/questions` does: at most four per call, each question's recommendation
first, then its alternatives, then "Drop it". In any other harness, print
`agent-plan questions PLAN --all` and tell the operator to answer them with
`/questions` in a Claude Code session. When they answer, record each answer
with `agent-plan decide PLAN Q1 --answer 'ANSWER'`, which settles the question
in place and logs `decided`; `--drop --answer 'why'` drops it, and
`--supersedes Q2` marks the earlier decision this one replaces. A decision
can make waiting questions askable, so list them again until none are
left. The operator may also run `/questions` in Claude Code themselves.

Ask the operator only about a one-way door (a force-push, deleted data, a
message sent outside, a migration, a stored format, a published interface)
or about product direction; only their answer settles such a question.
The `grill` skill is the exception: once the operator invokes it, every
open choice goes to them. Otherwise decide every other matter yourself,
and record each such reversible decision of your own in one step, already
decided, where the operator can reopen it:

```sh
agent-plan ask PLAN --question 'Keep the old flag?' --recommendation 'no, drop it' \
  --answer 'no, drop it: nothing reads it' --role R
```

List these decisions in the hand-back. A fact that a
command, a file, the docs or a sub-agent's lookup can establish is yours to
find; write what you found into the question it informs. Each question
holds one idea, worded so that "yes" accepts your recommendation, and the
most important comes first.

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
watcher's and `agent-plan reseal` is the operator's, and the guard denies
both. A log refused as changed stays as it is: name it in your report.

## Spikes

When running code is the quickest way to answer a design question, write
the question with `agent-plan ask` first, then answer it with throwaway code
on a local branch `spike/<slug>`, never pushed and never merged. Settle
the question as "Questions for the operator" says, its answer saying what
the spike showed and naming the branch.
The real change is then written fresh under the plan; the spike's code is
not carried into it.

## Claude Code's plan mode

A plan approved in plan mode is written into the session's plan's Goal
and Steps, with `agent-plan section` and `agent-plan step`, before the
first edit, and its questions and decisions through `agent-plan ask`.

## Plan and halt

If the operator asks you to plan, write the plan and stop: no edits outside the
plan's files, and no commits. Log `planned` for the plan's ID, run
`agent-plan set PLAN STATUS halted`, and report the plan's path with its open questions.

## The pipeline

The plan is the first step of the `review-change` skill's pipeline, which
runs the rest and records each stage here.
