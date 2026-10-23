---
name: validate-at-the-boundary
description: Use when code takes input from outside itself (flags, configuration, files, the network, another program's or a model's output).
---

# Validate at the boundary

Check input where it enters (flags, config, files, the network, another
program's output) and parse it into types that make illegal states
unrepresentable; code inside trusts those types.

**The test:** each value from outside is checked once, where it enters,
and the code past that point takes a type that cannot hold an invalid
value.

You skipped this when the same check repeats in several consumers, or a
raw string or map travels inward to be checked, or not, where it is used.
