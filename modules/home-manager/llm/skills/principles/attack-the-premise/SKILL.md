---
name: attack-the-premise
description: Use when two fixes for the same failure have failed, or a review finding returns after its fix, before writing another fix.
---

# Attack the premise

After two fixes have failed, suspect the premise both assumed, not the
fixes. Write it down as one sentence, as a hypothesis in the plan, and test
it before a third fix: find an observation that would come out otherwise
if the premise were false, and make it. If it holds, ask what caused it;
if it fails, the cause is elsewhere, so look at what the failures share
that the premise did not predict.

**The test:** before the third fix, the plan holds the premise as a
hypothesis with the probe that tested it and its result.

You skipped this when a third fix of the same kind (another timeout,
another retry, another special case) lands while the premise both earlier
fixes assumed is written nowhere.
