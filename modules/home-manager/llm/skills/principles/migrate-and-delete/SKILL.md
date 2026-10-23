---
name: migrate-and-delete
description: Use when changing an interface, a format, a flag or a name that something else calls, reads or stores.
---

# Migrate and delete

When an interface changes, move every caller and delete the old form in the
same pass. Callers outside our control, such as a published API or data
already stored, get expand–contract instead: the new form beside the old,
callers moved over in batches that each pass on their own, the old form
removed last.

**The test:** after the change, a search for the old name or form finds
only what expand–contract still serves, and each of those is a caller
outside our control.

You skipped this when a caller in this repository still uses the old form,
or is left for a later change.
