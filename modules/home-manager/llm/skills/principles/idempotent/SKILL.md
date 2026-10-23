---
name: idempotent
description: Use when writing or reviewing an operation that changes state, such as a migration, an activation step, a hook, a setup script or a retry.
---

# Idempotent

An operation that changes state ends in the same state however often it
runs. Ask what happens if it runs twice, and if the last run stopped at any
point; where the answer depends on what was left behind, it first
reconciles with that.

**The test:** run it twice, and once stopped at each step and run again:
every run ends in the state one clean run leaves.

You skipped this when a second run fails on something that already exists,
adds a duplicate entry, or depends on a file the last run left half
written.
