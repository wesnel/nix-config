---
name: questions
description: Ask the operator the open questions of this session's plan, and record their answers as decisions. Only when the operator invokes it.
disable-model-invocation: true
---

# Questions

1. Run `agent-plan find`; its first result's `plan` is PLAN. With none, say
   this session has no plan, and stop.
2. Run `agent-plan questions PLAN --all --json`: the questions the operator
   can answer now. With none, say so, and stop.
3. Ask them in order with the harness's question tool, as many per call as
   the tool takes. Each question's header is its `id`, then ` [#P]` with its
   `priority` if it has one; its text its `question`, then its `context` if
   any. Its options: the `recommendation` first, labelled with
   " (Recommended)" after it, then its `alternatives`, then "Drop it",
   dropping alternatives from the end to fit the tool's limit. The tool's
   own free-text answer takes any other. A question with no recommendation
   and no alternatives is asked as text, as where there is no tool.
   - **Claude Code:** AskUserQuestion, at most four questions per call, two
     to four options each, a header of at most 12 characters. It adds
     "Other" itself.
   - **Codex, Plan mode:** `request_user_input`, at most three questions per
     call, two or three options each, a header of at most 12 characters. It
     adds "Other" itself. Plan mode forbids changing anything, so instead of
     step 4, show the operator the `agent-plan decide` command for each
     answer, and run them once they leave Plan mode, unless they run them
     themselves.
   - **Codex, Default mode:** no tool. Ask one question per message, in
     plain text: its ID, the question, the recommendation, and the
     alternatives in a sentence. Record that answer as step 4 says, then ask
     the next.
   - **ECA:** `ask_user`, one question per call, two to four options, with
     `allowFreeform` left at its default. It has no header: start the
     question's text with the ID and priority.
   - **Copilot CLI:** `ask_user`, one question per call; it offers a custom
     answer itself. Autopilot answers it without the operator, so in
     autopilot say so, and stop.
   - Where the harness has no such tool, print each question with its ID,
     context, recommendation and alternatives, and take the operator's
     answers from their reply.
4. Record only an answer the operator gave. A question cancelled, declined,
   left empty, timed out or refused by the harness stays open: record
   nothing for it. For "Drop it", chosen by the operator, run
   `agent-plan decide PLAN ID --drop --answer 'dropped by the operator'`,
   or with their reason if they gave one. For any other answer, run
   `agent-plan decide PLAN ID --answer 'ANSWER'`, the answer on one line,
   without " (Recommended)".
5. A decision can make a question that waited for it askable: run step 2
   again, and ask from step 3 the questions it lists that were not asked
   yet, until it lists no new one.
6. Report each decision in one line: its ID, the question, the answer; and
   the IDs of any left open.
