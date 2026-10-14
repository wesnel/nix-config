---
name: grill
description: Map the decisions a plan leaves open and write them into it as rounds of questions for /questions.
disable-model-invocation: true
---

# Grill a plan

Settle every choice a plan leaves open before the work starts, by putting
the choices to Wesley in rounds. The questions live in the plan and reach
him as the `plan-work` skill's "Questions for Wesley" says.

1. Find the plan with `agent-plan find` and read it, Goal and Decisions
   first. With none, make one as the `plan-work` skill says.
2. Map the decision tree: every choice the Goal, Steps or Distribution
   depends on that Decisions does not settle, and for each, the choices
   that only make sense once it is settled.
3. Look up every fact the tree needs, as "Questions for Wesley" says. Run
   slow lookups in the background, and write the questions that do not
   depend on them meanwhile.
4. Write each remaining choice as an Open question in the `plan-work`
   skill's form, with the IDs of the questions or steps it depends on in
   `:AFTER:`.
5. Tell Wesley only how many questions he can answer now
   (`agent-plan questions PLAN --all --count`); in Claude Code,
   `/questions` asks them.
6. When answers come back as decisions, walk the tree again: reword the
   questions an answer changes, and add those it raises. One it makes moot
   stays Wesley's to close: recommend dropping it, naming the decision.
7. Done when `agent-plan questions PLAN --all --waiting` lists nothing and
   every choice in the tree is under Decisions.
