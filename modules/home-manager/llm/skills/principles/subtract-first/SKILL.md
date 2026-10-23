---
name: subtract-first
description: Use before adding code, an option, a step or a doc beside something the addition replaces in whole or in part.
---

# Subtract first

Delete what the change makes obsolete before building on what is left: the
old code path, the option nothing will set, the doc of the replaced
behaviour, the tests of a removed module.

**The test:** after the change, nothing in the tree serves only the
behaviour the change replaced.

You skipped this when the change replaces something and its diff removes
nothing, or when the old form stays beside the new one with no caller
outside our control to need it.
