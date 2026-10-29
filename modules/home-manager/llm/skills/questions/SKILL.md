---
name: questions
description: Present the open questions of a plan to the operator and record their answers as decisions. Use when the operator asks for the questions, and in Codex when a question becomes askable and a native question tool is offered.
disable-model-invocation: true
---

# Questions

1. PLAN is the plan the operator names, else the plan of the current task,
   else the one `agent-plan find` lists. With several and no way to tell
   which, list their paths and stop for the operator to choose; with none,
   say this session has no plan, and stop.
2. Run `agent-plan questions PLAN --all --json`: the questions the operator
   can answer now, in order. Leave out those already waiting for an answer
   in this session. With none, say so if the operator asked, and stop.
3. Present them in that order, as the harness allows below. Each one's
   header is its `id`, then ` [#P]` when it has a `priority` P; its text
   its `question`, then its `context` if any. Its options: the
   `recommendation` first, labelled " (Recommended)", then its
   `alternatives`, then "Drop it", dropping alternatives from the end to
   fit the tool's option limit. The tools add a free-text answer
   themselves.
   - Claude Code: `AskUserQuestion`, at most four questions per call.
   - Codex: `request_user_input_async` where it is offered, within its
     limits; it queues the questions and returns, so go on with work that
     does not depend on their answers. Where only `request_user_input` is
     offered and the current mode allows it, use that. With neither, and
     only when the operator asked for the questions, list them in your
     reply and take the answers there.
4. Only an explicit answer settles a question; a queued call, silence or an
   empty result leaves it open. Before recording an answer, read the
   question again from PLAN: if it is already settled, keep that decision;
   if its wording changed, or the answer could belong to another question,
   ask the operator which before recording it.
   - "Drop it":
     `agent-plan decide PLAN ID --drop --answer 'dropped by the operator'`,
     or with their reason if they gave one.
   - Any other answer: `agent-plan decide PLAN ID --answer 'ANSWER'`, the
     answer on one line, without " (Recommended)".
5. A decision can make a question that waited for it askable: run step 2
   again and present the new ones, until it lists none not yet presented.
6. Report each decision in one line: its ID, the question, the answer.
