---
name: plan-work
description: Keep a plan file and its event log for a change with agent-plan - where plans live, their format and item IDs, what to log and when, finding a plan again after a compaction, a resume or a hand-off, and planning without acting when Wesley asks only for a plan. Use before starting any change, when picking one up again, and whenever Wesley asks for a plan.
---

# Plans

Every change has a plan: a directory made by `agent-plan new` under the plans
directory, holding `plan.org`, a `<part>.org` per sub-agent's part, and
`log.org`. You and whoever picks the work up after you work from it, so keep
it true.

## Making one

```sh
agent-plan new --title 'Add the run loop' --slug add-run-loop \
  --repo owner/name --branch main..add-run-loop
```

It prints the plan's directory (`plan`), its file, its ID and its log. The
worktree and, in Claude Code, the session are recorded from where it runs;
pass `--worktree` and `--session HARNESS:ID` otherwise. Plans are never
committed: they stay out of every repository, the one around the plans
directory included.

A sub-agent given part `P1` of a plan makes its own file beside the parent's
and logs to the same `log.org`:

```sh
agent-plan new --parent PLAN --part P1 --title 'Add the run loop flag' --branch add-run-loop..run-loop-flag
```

## The plan file

- `* Plan`, whose properties say where the work is: `ID`, `HOST`,
  `REPOSITORIES`, `BRANCHES` (`base..branch`), `WORKTREES`, `SESSIONS`
  (`harness:id`), `HARNESS`, `STATUS` (`planning`, `reviewing-plan`,
  `working`, `reviewing`, `handed-back` or `halted`), `PULL_REQUESTS` and
  `PIPELINE` (`full` or `opted-out:<reason>`). Lists are separated by spaces.
- `** Goal`: the goal and what is not this change's job, and nothing of how
  the work went. It is the fresh review's goal statement, word for word:
  `agent-plan goal PLAN [--part P1]` prints it.
- `** Decisions`: a heading per decision, with `:ID: D1`, `D2`…
- `** Steps`: `*** TODO <step>` headings with `:ID: S1`, `S2`…; a part's
  are `P1.S1`, `P1.S2`…
- `** Distribution`, in a plan split among sub-agents: a heading per part
  with `:ID: P1`…, its branch, base and worktree, a link to `P1.org`, and
  its steps.
- `** Open questions`: `:ID: Q1`…, each with your recommendation. A settled
  one becomes a decision.
- `** Verification`: how the change is shown to work.

An ID is never reused or renumbered; a step dropped from the work keeps its
ID and gets an `abandoned` event. Edit only your own plan file, and change
it in the same step as what it describes: a new step, a decision, a moved
base.

## The log

`log.org` is append-only, and only `agent-plan log` writes it; the guard
denies any other write, and a log changed some other way is refused from
then on.

```sh
agent-plan log PLAN --item S1 --kind started
agent-plan log PLAN --item S1 --kind done --commit 'Add the run loop' --evidence 'go test ./... passes'
agent-plan log PLAN --item S1 --kind verified --evidence 'red-check: fails-as-asserted'
```

- Log `started`, `done` and `verified` for every step. `verified` names
  evidence you observed, never one you expect.
- `--commit` takes the commit's subject: the signing pass changes every SHA.
- An event about the whole plan names the plan's ID; about a part, the
  part's.
- `--role` is yours: `single`, `parent` or `subagent`.
- Which model and effort to start a sub-agent with is the
  `coordinate-subagents` skill's "Model and effort".
- The review pipeline's events are the `review-change` skill's.

`agent-plan status PLAN` lists the steps without `done` and those done but
not `verified`.

## Finding it again

After a compaction, a resume or a hand-off, find the plan before anything
else, then read it and `agent-plan status PLAN`:

- Claude Code names it when the session resumes or compacts.
- Elsewhere, `agent-plan find` lists the plans that match this session,
  worktree or branch; `--session`, `--worktree` or `--branch` matches the
  one given instead.
- A sub-agent's task names its plan and part.

Only this host's plans are yours to change. Another host's directory beside
it is a mirror, overwritten from that host.

## Claude Code's plan mode

A plan approved in plan mode is written into the session's plan's Goal,
Steps and Questions before the first edit.

## Plan and halt

If Wesley asks you to plan, write the plan and stop: no edits outside the
plan's directory, and no commits. Log `planned` for the plan's ID, set
`STATUS` to `halted`, and report the plan's path with its open questions.

## The pipeline

The plan is the first step of the `review-change` skill's pipeline, which
runs the rest and records each stage here.
