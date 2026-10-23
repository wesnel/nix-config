---
name: model-the-domain
description: Use when choosing the types, fields and state a change keeps, before writing the logic that uses them.
---

# Model the domain

Name the data's shape before the logic: the states a thing can be in and
the moves between them, as one type. A second boolean kept in step with a
first is a state machine: one type with a value per state.

**The test:** each combination of values the type allows is a state the
domain has, and each state the domain has is one value.

You skipped this when code checks for combinations of flags that should
never occur, or a comment says which fields go together.
