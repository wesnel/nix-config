---
name: design
description: The principles and checks for designing a change so the next one stays local. Use when planning a change's structure, choosing where code, state or a rule lives, changing an interface, and in every design review.
---

# Designing for change

Each principle is a skill of its own. Load each of these before planning a
change's structure, and make the checks below too; the `review-change`
skill's design review checks against both:

- the `subtract-first` skill;
- the `smallest-diff` skill;
- the `tool-selection` skill;
- the `from-scratch` skill;
- the `one-source-of-truth` skill;
- the `extend-a-seam` skill;
- the `migrate-and-delete` skill;
- the `idempotent` skill;
- the `separate-before-serializing` skill;
- the `validate-at-the-boundary` skill;
- the `model-the-domain` skill;
- the `minimize-reader-load` skill;
- the `design-it-several-times` skill, while the plan leaves an interface
  undecided.

## Checks

Make each of these while planning:

- For each condition that infers what happened before (a file or
  directory present or absent), name every code path or command that
  creates it; if anything besides the event the condition is meant to
  detect creates it, the inference is wrong.
