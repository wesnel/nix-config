---
name: retro
description: Propose changes to the agents' environment - guard rules, hooks, checks, reviewer checks, skill and AGENTS.md wording - that would have prevented a session's slips, and instructions to delete because they change nothing. Use at the review-change hand-back for every finding settled fix, after work that went wrong, and when the operator asks for a retro.
---

# Retro

A retro changes what the next session works with, never this session's
code. It only proposes: every change it finds is a question for the
operator, and you make none of them, however small, to a guard rule, hook,
skill, AGENTS.md or memory.

## Read first

- The plan: `agent-plan status PLAN` and its log, for `blocked` and
  `abandoned` events, steps done more than once, stale steps, commits no
  step records, and findings settled `fix`.
- The session's transcript, where the harness keeps one (Claude Code's
  `~/.claude/projects/<project>/<session>.jsonl`, Codex's
  `~/.codex/sessions/<date>/rollout-*.jsonl`): refusals, retries and
  the operator's corrections.
- The harnesses' memory files: Claude Code's
  `~/.claude/projects/<project>/memory/` (`MEMORY.md` and the files it
  indexes) and, where its `memories` feature is on, Codex's
  `~/.codex/memories/` (`MEMORY.md`, `memory_summary.md`). A lasting
  lesson there belongs in the config at the place it constrains (a skill,
  AGENTS.md, a check): propose that move for each, and deleting the memory
  once the move lands.

## Each slip

A slip is anything that went wrong or cost a turn: a finding settled `fix`,
a refusal, a correction from the operator, a broken rule, a lookup that took
many calls. First look for an existing check that should have caught it:
one that is broken, too narrow or not wired in is the finding. Then
classify it:

- **Mechanical**, decidable without judgement (a command's shape, a path, a
  missing flag, a trailer): propose a check. A `pr-loop guard` rule with
  deny and allow examples, a hook, `git red-check` settings, a linter rule
  or a CI job.
- **Judgement**, decidable only by reading the change: propose a check for
  the reviewer (`reviewer.md`) or the design review, worded as something
  either can find in a diff.
- **Missing fact**, one the agent could not look up: propose where it
  belongs, next to what it constrains.

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

Write each proposal into the plan with `agent-plan ask`, the most costly
slip first: the exact change (file and wording, or the rule with its
examples) as the recommendation, and as context the slip with its evidence
(log event, transcript line or finding), its class and what the change
would have prevented. The operator answers them as any other question.
