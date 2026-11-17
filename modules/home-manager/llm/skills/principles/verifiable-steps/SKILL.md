---
name: verifiable-steps
description: Use when ordering work into steps, commits, branches or parts, when a migration or a run of similar edits has many units, and when part of the work is blocked.
---

# Verifiable steps

Break the work into units that each end in a state you can check, name the
check before the unit starts, and run it before starting the next: a break
found at the unit that caused it is cheap to place.

The first unit is a tracer bullet: the thinnest path through every layer
the change touches, running end to end, kept and grown by each later unit.
When something blocks a unit, the largest part buildable without it is a
unit of its own, built now.

Order the commits so the history argues for the change, each building and
passing on its own: a deletion before the reshape it clears the way for, a
test beside the behaviour it shows. A refactor the change needs goes
first: the bottom branch of the stack, or the first commit of a single
branch, with no test changed.

**The test:** each commit, checked out alone, builds and passes the check
its step named, and the first already runs end to end.

You skipped this when a batch of edits is checked only at the end, a
commit builds only with the one above it, a refactor shares a commit
with the behaviour it made room for, one layer is finished before any path
runs through the rest, or a blocked unit waits whole while part of it
could be built.
