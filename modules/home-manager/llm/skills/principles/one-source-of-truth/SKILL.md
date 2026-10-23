---
name: one-source-of-truth
description: Use when a fact, rule, value or piece of state would be written in more than one place, in code, configuration, docs or skills.
---

# One source of truth

Keep each rule in one place, which everything else reads. Where two
consumers need the same value, derive one from the other or generate both
from one definition; a copy drifts the first time one side changes.

**The test:** for each fact the change touches, you can name the one place
to edit, and editing it there changes every reader.

You skipped this when changing one fact takes edits in two files to stay
consistent, or when the only thing keeping two copies in step is a comment
asking for it.
