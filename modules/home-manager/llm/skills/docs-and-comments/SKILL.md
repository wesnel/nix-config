---
name: docs-and-comments
description: How to write documentation and code comments. Use before writing or changing a doc comment, a code comment, a README, an option description, a skill or AGENTS.md.
---

# Documentation and comments

Say what someone needs in order not to break things. Do not record how the current
state came about.

- **Describe what is.** Describe things as they are, never what they used
  to be: no decision narratives and no version archaeology.
- **Do not document an absence.** Explaining why a section, option or abstraction
  is *not* there is noise; the reader is not looking for it.
- **Temporal state does not belong in the repository.** "Not yet wired up",
  "today", "before the first release", "once X lands" all rot the moment someone
  acts on them. That content belongs in the pull request description or the
  ticket, which expire naturally.
- **Put a constraint next to the thing it constrains** — the config key, the
  function, the workflow step. Collected lists of gotchas drift out of sync and
  duplicate what is already written at the point of use. A central document should
  hold only what has no such home.
- **Keep contracts true.** A change that makes a doc comment, an option
  description, a skill or a README untrue corrects it in the same commit. A
  stale contract misleads more than a missing one.
- **Write plainly.** One term per concept, reused exactly; code is named by its
  exact identifier. No filler or hype: leverage, robust, seamless, powerful,
  simply, just, easily. Prefer the active voice, and state the condition before
  the action: "if the cache is stale, rebuild it". Define a term before the
  text relies on it.

For a skill, AGENTS.md or any other text an agent loads, also load the
`writing-for-agents` skill.

## Comments in code

Write as few comments as the code needs, however many the surrounding code
has: comment density is the one thing not to match. In lines you change, drop
comments that break these rules.

- Make the code obvious enough to need no comment, through names and
  structure. A comment never restates what the code shows, so comments inside
  a function body are rare.
- A doc comment covers inputs, outputs, side effects, performance, gotchas
  (including what they mean for tests) and relationships the code does not
  show, such as a Go type satisfying an interface. Leave the rest to the code.
- Give a reason only where a plausible change would do serious harm, to stop
  that change.
- Name nothing that changes or expires: no line numbers, spec or document
  section numbers, plan files, ticket numbers, dates, times or people. The one
  exception is a `TODO` for something plainly missing or broken and left for
  later: `TODO(ABC-123)` with the ticket's ID where the project tracks work in
  a ticket system such as Linear; otherwise a plain `TODO`, and a question in
  the plan asking the operator whether to file a ticket.
- Comments follow "Write plainly" above.
