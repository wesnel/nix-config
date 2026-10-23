---
name: extend-a-seam
description: Use before adding an interface, a layer, an option, a hook or an abstraction.
---

# Extend a seam

Extend an existing seam before adding a new one, and abstract on the second
real use, not the first. Nothing speculative: no option, layer or interface
without a current second user. Seams are named and narrow, with their
contract written at the seam: the types, invariants, ordering, errors,
configuration and cost a caller relies on.

**The test:** each new seam has a second caller or implementation in this
change, and no existing seam could have carried the change.

You skipped this when an option has one value set anywhere, an interface
is built for callers that do not exist, or a second mechanism sits beside
one that could have taken the new case.
