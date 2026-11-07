---
name: brag
description: Record brag-worthy work as it happens, as a brag item the operator approves for their brag sheet. Use when work solves a problem for others, measurably improves something, ships a tool others adopt or finds a hard bug; at the review-change hand-back; and when moving approved items into the brag file.
---

# Brag

The brag sheet, `brag/YYYY.org` in the notes repository, holds the impact
the operator would cite in a performance review: one entry per merged
change, with its pull request, repository, plan and a summary the operator
approved. Agents feed it with brag items, questions of kind `brag` in the
change's plan.

## Brag-worthy

Work is brag-worthy when the operator could cite it to someone judging
their impact:

- a problem solved for others: a team unblocked, a recurring failure gone;
- a measurable improvement, with its number: faster, cheaper, fewer pages;
- a tool or change others adopted;
- a hard bug found, especially one others had missed.

Routine work, such as a rename, a dependency bump or a fix of the change's
own mistake, is not.

## Record it as it happens

Record an item the moment the work shows its impact, while the evidence is
at hand, as the `retro` skill has papercuts recorded; never wait for the
hand-back. First check `agent-plan open --kind brag` and the plan's
questions for an item already recorded for the same change, and refer to
its ID rather than recording a second: each change gets one entry.

```sh
agent-plan ask PLAN --kind brag --question 'Made the plan sync survive a sleeping Mac' --recommendation 'Agents on the devbox keep working when the Mac sleeps; three stalls a week before, none since.' --context 'Evidence: the sync log over the last week.'
```

- `--question` is the entry's title: what changed, as a short heading.
- `--recommendation` is the drafted summary, on one line: the impact, who
  it helped and the evidence, such as a number.
- `--context` holds the evidence for the operator, which the entry leaves
  out.
- Where the change is a part of a plan, add `--part P1`, so the item sits
  beside the part's own pull request.

What the summary may carry depends on the repository:

- An employer's repository: the summary is the operator's own words about
  the impact, and the entry carries only it and the pull request link.
  Name no visual, and quote no code, diagram, screenshot or customer data,
  so nothing of the employer's outlives the operator's access.
- A personal or public repository: the summary may name the entries of its
  plan's Visuals section it rests on (V1, V2), which the entry's PLAN link
  reaches; copy none of their content.

## Approval

The operator approves or edits the summary as any other question, through
the `questions` skill; their answer is the summary kept. Only
`agent-plan brag` writes the brag file, and only from an approved item.

## Moving approved items into the brag file

Each demo digest run moves them, on whichever host runs it, once each
change has merged:

1. `agent-plan brags` lists, as JSON, the approved items no brag file holds
   yet, each with its plan file, ID and the pull requests of the file that
   holds it.
2. For each item whose change merged, run
   `agent-plan brag PLAN QID --pr OWNER/NAME#N --date YYYY-MM-DD`, with the
   item's merged pull request and the day it merged. An item whose change
   has not merged stays listed for a later run.

Done when `agent-plan brags` lists only items whose change has not merged.
