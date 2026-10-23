---
name: separate-before-serializing
description: Use when concurrent actors (sub-agents, processes, hosts, branches) would write the same file, key, branch or state.
---

# Separate before serializing

When concurrent actors would write the same file, key, branch or state,
give each its own and merge where they are read. Serialize with a lock, a
single writer or ordered phases only where one shared target is a real
invariant; a written convention does not serialize anything.

**The test:** for each actor, name what it writes. No two write the same
target, or a lock, a single writer or an order enforced by code keeps them
apart.

You skipped this when actors' writes conflict or get lost, and the fix is
an instruction telling them to take turns.
