---
name: explain-a-number
description: Use before reporting or acting on a number you measured, such as a speedup, a regression, a latency, a size or an eval result.
---

# Explain a number

Before reporting a measured number, name what bounds it (a profile or
counters from the run), rule out errors, cached or skipped work, code that
never ran and noise, and give its run count and spread. Answer each of
these from a run, not from reading the code:

1. Why is it not twice as good? Name the limit (a core, a lock, the disk,
   the network, the load generator) from a profile taken in a run you do
   not report.
2. Did each side run as it runs in use: the same build mode, flags, data
   and cache state? A side left on its defaults compares configurations.
3. Does the arithmetic allow it? A result past the bandwidth, the core
   count or the share of the time the changed part took measured something
   else.
4. How many requests or runs failed, and were the outputs correct?
5. Does it repeat? At least five runs a side, alternating, with the median
   and range; a gap inside the run-to-run spread is no difference.
6. Does it matter end to end, on the path someone waits on?
7. Did the work happen inside the timed region at all?

**The test:** the number is reported with its unit, its run count, its
spread and the limit that bounds it.

You skipped this when a number has no run count, no spread or no named
limit, or when the time saved exceeds the time the changed part took.
