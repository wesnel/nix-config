---
name: retro
description: Capture papercuts as they occur and propose improvements to the agents' tools, instructions and workflows. Use when friction appears, including a broken hand-off command, a help lookup for a nix-config tool or an unproductive review round; at the review-change hand-back for findings settled fix and session papercuts; and when the operator asks for a retro.
---

# Retro

A retro changes what the next session works with, never this session's
code. It only proposes: every change it finds is a question for the
operator, and you make none of them, however small, to a guard rule, hook,
skill, AGENTS.md or memory.

Papercuts are observed annoyances or opportunities to improve the workflow,
even when the task succeeds. Capture them proactively as they occur, while
the evidence is at hand, using Report below. Recording one does not wait
for a failed task, a review finding or the hand-back. Use the incident's
evidence for immediate capture; the wider reading below is for a full retro.

## Read for a full retro

- The plan: `agent-plan status PLAN` and its log, for `blocked` and
  `abandoned` events, steps done more than once, stale steps, commits no
  step records, and findings settled `fix`.
- The session's transcript, where the harness keeps one (Claude Code's
  `~/.claude/projects/<project>/<session>.jsonl`, Codex's
  `~/.codex/sessions/<date>/rollout-*.jsonl`): refusals, retries and
  the operator's corrections, help lookups and work that added no value.
- The harnesses' memory files: Claude Code's
  `~/.claude/projects/<project>/memory/` (`MEMORY.md` and the files it
  indexes) and, where its `memories` feature is on, Codex's
  `~/.codex/memories/` (`MEMORY.md`, `memory_summary.md`). A lasting
  lesson there belongs in the config at the place it constrains (a skill,
  AGENTS.md, a check): propose that move for each, and deleting the memory
  once the move lands.

## Each slip or papercut

A slip is anything that went wrong or cost a turn: a finding settled `fix`,
a refusal, a correction from the operator, a broken rule, a lookup that took
many calls. A papercut can be smaller: one avoidable lookup, an awkward
hand-off, or a step whose cost did not buy useful evidence.

For example:

- A handed-off `!` one-liner fails: capture the command, the shell or
  harness that ran it, and the failure. Propose a change at the source of
  the bad command, so the next hand-off works.
- You need `--help` for a tool maintained in nix-config: consider whether
  the current use case should be more obvious in the command interface,
  diagnostics or the skill that sends agents to it. Record what was hard
  to discover and the improvement that would remove that friction; a help
  lookup is evidence to examine, not by itself a defect.
- A review round yields no useful results: examine what it was meant to
  establish, what evidence it added, and what work it repeated. Consider
  narrower conditions for running it, combining it with another review,
  or removing it. A clean review can provide useful assurance; no findings
  alone does not establish waste. Propose changes to future review
  conditions while following the current task's requirements.

First look for an existing mechanism that should have prevented the issue:
one that is broken, too narrow or not wired in is the finding. Prefer
repairing or simplifying it before adding a rule. Then classify the issue:

- **Mechanical**, decidable without judgement (a command's shape, a path, a
  missing flag, a trailer): propose a check. A `pr-loop guard` rule with
  deny and allow examples, a hook, `git red-check` settings, a linter rule
  or a CI job.
- **Judgement**, decidable only by reading the change: propose a check for
  the reviewer (`reviewer.md`) or the design review, worded as something
  either can find in a diff.
- **Missing fact**, one the agent could not look up: propose where it
  belongs, next to what it constrains.
- **Workflow friction**, avoidable effort or a step that adds no value:
  propose a clearer tool or hand-off, or simplify, narrow or remove the
  step. Say what useful outcome the revised workflow must preserve.

A new rule lands with its existing violations fixed, or with a check that
keeps them from coming back: propose one or the other with it.

A repository that runs no check where one was needed, such as no tests or
lint in CI, is itself a finding.

## No-ops

Read what every session loads (AGENTS.md, skill descriptions) for sentences
the agents already follow untold, and rules a guard already enforces.
Propose deleting each whole sentence, with the evidence: a session that
behaved so without it, or the rule that enforces it.

## Report

Check the plan's questions and `agent-plan open --kind retro` for an
existing proposal first; refer to its ID instead of filing the same issue
again at hand-back or on a repeated occurrence.

Write each new proposal into the plan with `agent-plan ask --kind retro`
as it occurs; in a full retro, start with the most costly issue. Put the
exact change (file and wording, command behaviour, or rule with examples)
in the recommendation. In the context record the attempted task, observed
friction and its evidence (log event, transcript line or finding), its
class, and what the change would prevent or save. Keep observed facts
separate from a suspected cause; when the cause is uncertain, recommend
the investigation needed to choose a fix rather than inventing one.

The operator answers these as any other question, through the `questions`
skill. At hand-back, include the proposals already recorded and any newly
discovered papercuts. `agent-plan open --kind retro` lists those still open
across this host's plans.
