---
name: questions
description: Present a plan's actionable questions and record explicit answers. Use when the operator requests questions, or when plan-work directs Codex to queue them.
disable-model-invocation: true
---

# Questions

Use the plan named by the operator or established for the current task.
Otherwise run `agent-plan find` and use its unambiguous matching plan.
With several plausible matches, show their paths and stop for the operator
to select one. With none, say there is no matching plan and stop.

Run `agent-plan questions PLAN --all --json` to select questions whose
dependencies are settled. Present them in that order, excluding questions
already awaiting a reply in this session's native question queue. Track
each queued question by its plan path and question ID in the session.
When queue history is unavailable after a resume, resurface open questions
on the operator's request rather than assuming they need another card.
With no actionable questions, say so when invoked by the operator.

Present each question's ID, question and relevant context, with the
recommendation first and marked recommended, then its alternatives and
"Drop it". Use the available tool's schema and option limits. Include the
plan title when questions from multiple plans are pending.

- In Claude Code, use `AskUserQuestion` when the operator requests
  questions: at most four per call and four options per question, trimming
  alternatives from the end to fit. The tool supplies free-text answers.
- In Codex, prefer `request_user_input_async` when available, with at most
  three questions per call. Queue actionable questions promptly, continue
  independent work, and wait for explicit replies before dependent work.
  An accepted tool call means the questions were queued, not answered.
  When only `request_user_input` is available and allowed in the current
  mode, use it. Otherwise present the questions in the current conversation
  and accept replies there; the operator stays in the same harness.

On a reply, re-read the question from its plan before recording it. If it
is already settled, preserve that decision. If its wording has changed or
the reply's plan or ID is ambiguous, clarify before deciding. Only an
explicit reply settles a question: preselection, silence, elapsed time and
an empty tool result leave it open.

For "Drop it", run:

```sh
agent-plan decide PLAN ID --drop --answer 'dropped by the operator'
```

Use their reason when supplied. For another answer, record their reply on
one line, without a UI-only recommended suffix:

```sh
agent-plan decide PLAN ID --answer 'ANSWER'
```

Re-run selection after each batch of answers; a decision may unblock more
questions. In Codex, queue newly actionable questions while preserving
those still awaiting answers. In Claude Code, continue the requested
question round until no new actionable questions remain. Report each
recorded decision with its plan, ID and answer.
