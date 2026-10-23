---
name: playbooks
description: Pick the plan template for a task, before `agent-plan new` - which kind it is (investigation, bug fix, slowdown, feature, refactor, prototype, measured improvement), the template that starts its plan with that kind's steps, and the skills those steps load. Use at the start of every task.
---

# Playbooks

Each kind of task has a plan template whose steps are that kind's stages,
each with its `:AFTER:` order and what marks it done. Start the plan from
it, then set the plan's `SKILLS` to the skills its steps load, so that they
come back after a compaction or a resume as the `plan-work` skill's
"Finding it again" says:

```sh
agent-plan new --title 'Fix the lost write' --slug fix-lost-write --template bug-fix
agent-plan set PLAN SKILLS diagnose-failure write-tests review-change
```

| Kind | Template | `SKILLS` |
| --- | --- | --- |
| A question to answer, changing nothing | `investigation` | none |
| A bug: a wrong output, a crash, a failing or flaky test | `bug-fix` | `diagnose-failure write-tests review-change` |
| A regression in speed or resources | `slowdown` | `diagnose-failure review-change` |
| New behaviour | `feature` | `design write-tests review-change` |
| A change of structure that keeps behaviour | `refactor` | `design write-tests review-change` |
| A design question running code answers fastest | `prototype` | none |
| A number to move, by attempts measured against a fixed harness | `measured-improvement` | `review-change` |

A task of none of these kinds starts without `--template`. One of two
kinds, such as a feature that needs a refactor first, is two plans, the
refactor's first.

The steps come from the template alone: copy no step's text into the plan
by hand, and add the steps the work needs beyond them with `agent-plan
step`. A step that does not apply is skipped with its reason, never
deleted:

```sh
agent-plan log PLAN --item S3 --kind abandoned --summary 'REASON'
```
