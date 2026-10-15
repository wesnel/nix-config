---
name: design
description: The principles for designing a change so the next one stays local. Use when planning a change's structure, choosing where code, state or a rule lives, changing an interface, and in every design review.
---

# Designing for change

Each bold phrase names a principle. The `review-change` skill's design
review checks these.

- **One source of truth.** Keep each rule in one place, which everything
  else reads.
- **Extend a seam.** Extend an existing seam before adding a new one, and
  abstract on the second real use, not the first.
- **From scratch.** Fold a new requirement in as if the design had started
  from it: work out what you would build knowing it, then carry that through
  every type, doc, example and skill it touches.
- **Subtract first.** Delete what the change makes obsolete before building
  on what is left.
- **Smallest diff.** Make the smallest change that solves the problem.
  Internal tooling with no users beyond this repository changes in place,
  with no shim, dual path or transitional state.
- **Migrate and delete.** When an interface changes, move every caller and
  delete the old form in the same pass. Callers outside our control, such as
  a published API or data already stored, get expand–contract instead: the
  new form beside the old, callers moved over in batches that each pass on
  their own, the old form removed last.
- **Idempotent.** An operation that changes state ends in the same state
  however often it runs. Ask what happens if it runs twice, and if the last
  run stopped at any point; where the answer depends on what was left
  behind, it first reconciles with that.
- **Separate before serializing.** When concurrent actors would write the
  same file, key, branch or state, give each its own and merge where they
  are read. Serialize with a lock, a single writer or ordered phases only
  where one shared target is a real invariant; a written convention does not
  serialize anything.
- **Validate at the boundary.** Check input where it enters (flags, config,
  files, the network, another program's output) and parse it into types
  that make illegal states unrepresentable; code inside trusts those types.
