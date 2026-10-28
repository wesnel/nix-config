---
name: tool-selection
description: Use when choosing or reviewing a language, dependency, framework, editor or other tool, including implementation choices during building.
---

# Tool selection

Choose tools by these operator preferences:

- **Language.** Prefer strongly typed languages deployed as statically
  linked binaries, or dynamically typed languages running in a virtual
  machine. Prioritize Go or Erlang/Elixir over Bash or Python for new
  programs.
- **Standard library.** Use the language's standard library whenever it
  meets the need. Introduce a dependency for a concrete gap it fills.
- **Battle-tested.** Prefer old, proven tools over new, shiny ones,
  whether choosing a language, dependency, framework, editor or other
  tool. Look for established solutions before re-solving a problem that
  past generations already solved.

These are defaults for tool choices. When a task calls for another choice,
state the concrete requirement or tradeoff that outweighs the preference.
Apply them when choosing tools; migrating existing work needs its own
task reason.

**The test:** for each tool choice, a builder or reviewer can identify
which preference it follows, or the concrete reason for departing from it.
