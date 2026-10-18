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
plan file holds `* Plan` with the sections below and, from plan mode,
`* Approved plan`; `agent-plan status` lists any other heading, an entry
without its ID and a question without a question's keyword as structure
drift. Undo drift you wrote. Report any other in your hand-back, and
never delete or rewrite the operator's sections to silence it, nor log
their history afresh: the log records events as they happen.

- `* Plan`, whose properties say where the work is: `ID`, `HOST`,
  `REPOSITORIES`, `BRANCHES` (`base..branch`), `WORKTREES`, `SESSIONS`
  (`harness:id`), `HARNESS`, `STATUS` (`planning`, `reviewing-plan`,
  `working`, `reviewing`, `handed-back` or `halted`), `PULL_REQUESTS`,
  `PIPELINE` (`full` or `opted-out:<reason>`), `REVIEWS` (the focused
  reviews the change needs, such as `security`), `SKILLS` (the skills the
  work runs under beyond this one, such as `coordinate-subagents` or
  `review-loop`, which you add) and, once `agent-plan own` sets one,
  `OWNERS` (`worktree=agent`). Lists are separated by spaces.
  `WORKTREES`, `BRANCHES`, `PULL_REQUESTS`, `OWNERS`, `REVIEWS` and
  `PIPELINE` follow the log, as step keywords do: never edit them by hand.
  Change one with `agent-plan set PLAN BRANCHES main..loop [--part P1]`,
  which logs the whole new value; `agent-plan status` lists a value the log
  does not give, and a References entry it does not hold, as property
  drift. Stop-check and the gates go by the log, so a `PIPELINE` edited in
  the file opts nothing out, and a route once in `REVIEWS` stays owed when
  taken out again; waive one with `agent-plan waive PLAN --route ROUTE
  --why TEXT`, which the operator sees before signing.
- Name the routes the change needs when writing the plan: `agent-plan set
  PLAN REVIEWS security` where it takes untrusted input, touches
  authentication, secrets, running programs, exposure, customer data,
  dependencies, or agent or tooling configuration.
- `** Goal`: the goal and what is not this change's job, and nothing of how
  the work went. It is the fresh review's goal statement, word for word:
  `agent-plan goal PLAN [--part P1]` prints it. It ends with a
  `Likely next changes:` line: one or two concrete changes expected after
  this one, not speculation, which the design review of the plan checks
  would each be a local edit. It holds text, no headings. Load the
  `design` skill before writing the steps: its principles are what the
  design review checks them against.
- `** Steps`: `*** TODO <step>` headings with `:ID: S1`, `S2`…; a part's
  are `P1.S1`, `P1.S2`… Each says what observable result marks it done,
  which its `verified` evidence then shows. Steps that must run in order
  go under a heading with `:ORDERED: t`, which holds those steps and no
  text of its own, and a step that waits on another
  item lists it in `:AFTER:`; `agent-plan status` lists the steps ready
  now. A step or part that cannot go on until a time, such as a quota
  window's reset, is postponed with
  `agent-plan postpone PLAN S1 --until '2026-10-20 18:30'`, which keeps it
  out of the ready lists and the agenda until then; `--clear` undoes it.
- `** Distribution`, in a plan split among sub-agents: a `*** TODO <part>`
  heading per part with `:ID: P1`…, its branch, base and worktree, a link
  to its file, and its steps. A part or step that builds a follow-up
  carries `:BUILDS:`, which follows the log as "Follow-ups" says: never
  edit it by hand.
- `** Questions`: every question for the operator and every decision, each with
  `:ID: Q1`, `Q2`… (`P1.Q1` in a part's file), written by `agent-plan ask`
  as "Questions for the operator" says, never by hand. An open one is `ASK`; a
  settled one stays where it is, `DECIDED` with its `:ANSWER:`, or
  `DROPPED`, or `SUPERSEDED` by the decision whose `:SUPERSEDES:` names
  it. A decision is always such a question, settled in place and never
  renumbered, moved or given an ID of another kind.
  `agent-plan decisions PLAN --all` lists the decisions. A suggestion
  rejected for a reason that will still hold, such as a constraint rather
  than a lack of time, is a decision too, with that reason, so a later
  design review finds it instead of proposing it again.
- `** Follow-ups`: work found while doing an item but outside this
  change's job, each with `:ID: F1`, `F2`… and the `:FOUND_IN:` item, as
  "Follow-ups" says.
- `** References`: each URL the operator gave the work, one entry each with
  `:URL:`, and a note of what it was used for and which decisions (Q IDs)
  it fed. In Claude Code, Codex and ECA every URL in their prompts is added
  with no note; add one they give otherwise with
  `agent-plan reference PLAN URL`. When you
  are done with a reference, write its note:
  `agent-plan reference PLAN URL --note 'The pstack principles; fed Q3, Q4.'`
- `** Verification`: how the change is shown to work: each seam the tests
  cross, and what each cannot catch. It holds text, no headings.

The `#+TODO:` lines `agent-plan new` writes define the keywords; Emacs and
`agent-plan` both read them, so leave them as they are. The ID patterns
(`S`, `P`, `Q`, `F`, with an optional `P<n>.` prefix) are what Emacs's
plan views select entries by.

A question or a part that cannot start before others are settled lists
their IDs in `:AFTER:`, separated by spaces (a question's through
`agent-plan ask --after`): a question is settled once its keyword is a
done one, a part or step once `done`, `verified` or `abandoned` is logged
for it. Until then the question is not put to the operator and the part is not
ready.

An ID is never reused or renumbered; a step dropped from the work keeps its
ID and gets an `abandoned` event. Edit only your own plan file, and change
it in the same step as what it describes: a new step, a decision (through
`agent-plan ask` or `decide`), a moved base (through `agent-plan set`).

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
- When the harness reports a sub-agent's tokens, log them on the event for
  its task: `--tokens N --model M --effort E`. Which model and effort to
  start it with is the `coordinate-subagents` skill's "Model and effort".
- The review pipeline's events are the `review-change` skill's.

`agent-plan status PLAN` lists the steps without `done` and those done but
not `verified`, the parts ready to start, the steps ready now, what still
waits on an `:AFTER:` item, keyword, property, link and structure drift, steps started with nothing
logged for a day, commits no `done` event names (a `fixup!`, `amend!` or
`squash!` commit counts as the one it names), and tokens by part, kind and
model; `agent-plan tokens` totals every plan's. Log `done` with the
`--commit` of each listed commit that completes a step, or say why none
does.

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
the brief counts from, and where the status line and the Stop hook suggest
/clear to the operator when the context is large or its cache cold.

Only this host's plans, the files named for it, are yours to change; another
host's beside them are read-only here, and `agent-plan` refuses to write
them.

## Questions for the operator

Every question for the operator lives in the plan's Questions, with your
recommendation, and nowhere else. Write each with `agent-plan ask`, which
gives it the next ID and the form `agent-plan questions` reads; a question
written by hand in another form never reaches the operator:

```sh
agent-plan ask PLAN --question 'Keep the old flag?' --recommendation 'no, drop it' \
  --context 'Nothing reads it.' --alternative 'keep it' --after Q1 --priority A
```

`--part P1` puts it in that part's file; `--priority A` puts it before the
others, `C` after. Never restate open questions in a message: when a
sub-agent reports, say only what changed, add its new questions with
`agent-plan ask`, and at most give their number. When the operator asks for
them, take `agent-plan questions PLAN --all --json`, the questions they can
answer now, and ask them through the harness's own question tool where it
has one, each question's ID as its header, its recommendation first with
" (Recommended)" after it, then its alternatives, then "Drop it", trimmed
from the end to the tool's limit:

- **Claude Code:** AskUserQuestion, at most four questions per call, two to
  four options each, a header of at most 12 characters.
- **Codex:** `request_user_input`, in Plan mode only, at most three
  questions per call, two or three options each, a header of at most 12
  characters. Plan mode forbids changing anything: give the operator the
  `agent-plan decide` commands for their answers, and run them once they
  leave it. In Default mode, ask one question per message in plain text.
- **ECA** and **Copilot CLI:** `ask_user`, one question per call; ECA's
  takes two to four options and has no header, so the ID starts the text.

Elsewhere, or for a question with fewer than two options, print
`agent-plan questions PLAN --all` and take the answers from the chat.
Record only an answer the operator gave, and "Drop it" only when they chose
it: a question cancelled, declined, left empty or refused by the harness
stays open. Record each answer with
`agent-plan decide PLAN Q1 --answer 'ANSWER'`, which settles the question
in place and logs `decided`; `--drop --answer 'why'` drops it, and
`--supersedes Q2` marks the earlier decision this one replaces. A decision
can make waiting questions askable, so list them again until none are
left. The operator can run these same steps themselves with `/questions`
(`$questions` in Codex) where it is installed, or settle one in Emacs, with
a note that `agent-plan decisions` reads as the answer.

Ask the operator only about a one-way door (a force-push, deleted data, a
message sent outside, a migration, a stored format, a published interface)
or about product direction; only their answer settles such a question.
Decide every other matter yourself, and record each such reversible
decision of your own in one step, already decided, where the operator can
reopen it:

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

A follow-up the operator approves becomes work of its own: a part, or a
step, that names it. Add the part to Distribution as for any part, and
make its file with the follow-up it builds; link an entry that already
exists with `link`:

```sh
agent-plan new --parent PLAN --part P3 --title 'Split the guard' --from F3
agent-plan link PLAN S4 P1.F2      # a step or part already in the plan
agent-plan link PLAN S4 --clear    # it builds none after all
```

A follow-up in a part's file keeps its part's prefix (`P1.F2`). Each logs
a `builds` event and writes the entry's `:BUILDS:` from the log; status
lists a `:BUILDS:` the log does not give as property drift. Only a
`TODO` or `STARTED` follow-up can be linked. Logging `done` or
`verified` for the entry logs the same for its follow-up, and linking an
entry already done or verified closes the follow-up at once, so never
close a linked follow-up yourself. A done or verified entry changes its
link only after `link --clear`, so a mistyped ID closes nothing.
Abandoning the entry releases the link for good, removing its
`:BUILDS:`, and leaves the follow-up open for another entry to build;
abandoning the follow-up releases its link too, and nothing its builder
logs changes it. A follow-up has one builder: `link` refuses a second,
and `agent-plan status` lists as link drift a link to a follow-up the
plan does not hold, one two entries build, one abandoned, or one behind
its done or verified builder; run the same `link` again to close that
last one, which logs no new link.

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

A plan approved in plan mode is recorded word for word in the session's plan
under `* Approved plan`, which stays as it is. Before the first edit,
structure it into Goal and Steps with IDs, and its questions and
decisions through `agent-plan ask`.

## Plan and halt

If the operator asks you to plan, write the plan and stop: no edits outside the
plan's files, and no commits. Log `planned` for the plan's ID, set
`STATUS` to `halted`, and report the plan's path with its open questions.

## The pipeline

The plan is the first step of the `review-change` skill's pipeline, which
runs the rest and records each stage here.
