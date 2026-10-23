---
name: design-it-several-times
description: Use while a plan leaves an interface undecided, before choosing one.
---

# Design it several times

While the plan leaves an interface undecided, design it at least three
times before choosing: one sub-agent that only reads per design, each
under a different constraint (the smallest interface; the commonest call
made trivial; the most callers served; the dependency behind a port), each
returning the interface with its contract, a call site and what it hides.
Compare them by how much each hides and how local each likely next change
stays, and choose as the `plan-work` skill's "Questions for the operator"
says. Where running code answers faster than a design on paper, answer it
with a spike, as that skill's "Spikes" says.

**The test:** the decision names the designs it was chosen over, and why.

You skipped this when the plan records one interface with no alternative,
or alternatives that differ only in their names.
