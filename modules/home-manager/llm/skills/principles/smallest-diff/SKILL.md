---
name: smallest-diff
description: Use when deciding how much a fix or feature changes, and before adding a shim, a fallback, a dual path or a transitional state.
---

# Smallest diff

Make the smallest change that solves the problem. Internal tooling with no
users beyond this repository changes in place, with no shim, dual path or
transitional state.

**The test:** every hunk is one the problem needs: reverting any one of
them leaves the problem unsolved or the tree broken.

You skipped this when the diff carries a rename, a refactor or a cleanup
the problem did not need, or keeps a compatibility path for a caller that
lives in this repository.
