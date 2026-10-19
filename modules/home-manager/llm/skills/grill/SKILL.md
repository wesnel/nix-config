---
name: grill
description: Map the decisions a plan leaves open and write them into it as rounds of questions for /questions.
disable-model-invocation: true
---

# Grill a plan

Settle every choice a plan leaves open before the work starts, by putting
the choices to the operator in rounds. The questions live in the plan and
reach them as the `plan-work` skill's "Questions for the operator" says.

1. Find the plan with `agent-plan find` and read it, Goal and decided
   questions (`agent-plan decisions PLAN --all`) first. With none, make one
   as the `plan-work` skill says.
2. Map the decision tree: every choice the Goal, Steps or Distribution
   depends on that no decided question settles, and for each, the choices
   that only make sense once it is settled.
3. Look up every fact the tree needs, as "Questions for the operator" says. Run
   slow lookups in the background, and write the questions that do not
   depend on them meanwhile.
4. Write each remaining choice with `agent-plan ask`, passing the IDs of
   the questions or steps it depends on as `--after`. Every choice goes to
   the operator, reversible ones included: invoking this skill is the
   operator asking to make them all. Decide none yourself.
5. Tell the operator only how many questions they can answer now
   (`agent-plan questions PLAN --all --count`); in Claude Code,
   `/questions` asks them.
6. When answers come back as decisions, walk the tree again: reword the
   questions an answer changes, and add those it raises. One it makes moot
   stays the operator's to close: recommend dropping it, naming the decision.
7. Done when `agent-plan questions PLAN --all --waiting` lists nothing and
   every choice in the tree is a decided question.
