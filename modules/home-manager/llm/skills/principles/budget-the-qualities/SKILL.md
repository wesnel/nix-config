---
name: budget-the-qualities
description: Use when writing a plan's Goal, and when reviewing a change for what it makes cost more, such as time, memory, size or calls.
---

# Budget the qualities

A change moves more than its behaviour. Name the qualities it moves, give
each a budget and one command that measures it, and measure before and
after, as a test does for behaviour.

1. Go through each quality and decide whether the change can move it:
   - `time`: latency, throughput, startup, and work run per request, per
     item, per keystroke or per tool call;
   - `memory`: peak and retained, caches, whole inputs read at once;
   - `size`: binary, bundle, closure, image, download, stored data;
   - `calls`: network requests, process spawns, disk writes, queries,
     and their timeouts;
   - `cost`: money, tokens, the context an agent loads every turn;
   - `failure`: error rate, availability, what a user sees when a
     dependency is slow or down;
   - `operability`: logs, metrics and errors someone uses to find a
     fault;
   - `accessibility`: who can still use the interface it changes.
2. Write the plan's `Qualities:` line: each quality moved, with its budget
   and its command, or that none moves and why. A budget is a number
   ("p50 under 20 ms", "at most four lines") or, with no target given, no
   worse than the baseline beyond its run-to-run spread. The command
   prints the number; for a quality no command measures, such as
   accessibility, the command is the check someone goes through, named
   by what it inspects.
3. Run each command or check for a baseline before changing the code, and
   again after, the same way. Report both numbers as the
   `explain-a-number` skill says, and both results of a check.
4. Make a budget that must outlast the change a check that can fail (a
   size limit in CI, a benchmark comparison, a test on the number of
   calls), shown failing on a deliberate violation.

The harness exercises the path someone waits on: a benchmark of a
helper the change does not slow, or of a cache the real path misses,
measures something else.

**The test:** the Goal's `Qualities:` line names each quality the change
moves with a budget and a command or check, and the plan's log holds each
one's result before and after.

You skipped this when a change adds work to a path run per request or per
item and the Goal says nothing moves, or when a budget has no baseline.
