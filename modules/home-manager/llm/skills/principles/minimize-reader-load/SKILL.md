---
name: minimize-reader-load
description: Use when shaping or reviewing code a reader must trace, and before adding a module, a wrapper, a layer or a piece of state.
---

# Minimize reader load

What code costs is the work its reader does: the layers between a question
and its answer, and the state they must hold in mind meanwhile.

- A wrapper with one caller, or an adapter with one implementation, is
  folded into what it wraps.
- A new module is worth keeping when deleting it would spread its
  complexity across its callers rather than move it, its interface is much
  smaller than what it hides, and following one concept does not mean
  hopping through many small modules.
- State lives in the narrowest scope that works: a return value before a
  mutation, a local before a field, a value derived before one kept in
  step.

**The test:** for each value, where it comes from and what can change it is
answerable in thirty seconds of reading.

You skipped this when a reader follows a call through layers that pass the
same arguments on, or a value set in one place is kept in step by hand in
another.
