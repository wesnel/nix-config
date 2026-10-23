---
name: reproduce-first
description: Use before fixing a failure (a bug, a failing or flaky test, a crash, a regression), and before calling one fixed.
---

# Reproduce first

The same reproduction shows the failure before the fix and its absence
after; for an intermittent one, give how often it fails before and after,
over the same number of runs. A retry, skip or loosened assertion that
hides a failure is not a fix unless the operator asked for one.

A fix:

- changes the cause. A guard where the symptom shows, such as a nil check
  or a fallback value, leaves the cause to fail somewhere else;
- covers every instance: search (`git grep`, `gopls references`) for the
  pattern the cause shows, and fix each place it recurs in the same commit.

**The test:** the report shows one command failing before the change and
passing after it, and names the cause the change removed.

You skipped this when a failure is called fixed on a passing run alone, or
the fix is a retry, a longer timeout, a skip, or a check at the place the
symptom showed.
