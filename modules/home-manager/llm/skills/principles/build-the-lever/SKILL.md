---
name: build-the-lever
description: Use when work repeats across many places or its result must be checked by someone else, such as a change across files, a migration, an analysis or a verification.
---

# Build the lever

When work is more than a few changes seen at a glance, write the tool that
does or checks it, and run that: a script, a query, a generator, a Nix
check, or a skill the sub-agents doing the work follow. A reviewer reruns a
tool; work done by hand can only be redone.

- Do the first unit by hand, then write the tool and run it on that unit:
  its result matches yours.
- Make it safe to run again, as the `idempotent` skill says.
- One run of a tool over every unit beats sub-agents each doing the same
  thing by hand.
- Files are still changed with the file-editing tools. For an edit across
  many files, the lever is what finds every place and shows each one
  changed, such as a `git grep` that must come back empty.
- Write the smallest tool that does the job. Keep it in scratch when the
  work ends with the session; commit it, as a check or a script, when what
  it checks must keep holding.

**The test:** the change's evidence includes the command that did or
checked the work, which a reviewer can run again.

You skipped this when a report says many places were changed alike and
names no command that shows it, or several sub-agents were each told to
apply the same change by hand.
