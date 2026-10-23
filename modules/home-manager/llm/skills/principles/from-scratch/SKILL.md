---
name: from-scratch
description: Use when a new requirement arrives for a design that already exists, before deciding where the change goes.
---

# From scratch

Fold a new requirement in as if the design had started from it: work out
what you would build knowing it, then carry that through every type, doc,
example and skill it touches.

**The test:** a reader who never saw the earlier shape cannot tell from the
result which requirement came last.

You skipped this when the new requirement lives in a special case, a flag
or an appended section, while the types, docs and examples around it still
describe the design without it.
