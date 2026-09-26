---
name: agent-shell-memory
description: Search past agent sessions for relevant context. Use when you need to recall prior decisions, patterns, or information from previous conversations in this project.
---

# Agent Shell Memory

Each project stores past agent session transcripts under `.agent-shell/transcripts/`. Use `grep` to search these transcripts for relevant context before starting work — prior decisions, patterns, or solutions may already exist.

## When to Use

- Before making architectural decisions, check whether this was discussed before.
- When asked about something that "was done before", search transcripts first.
- When encountering a repeated problem, check for prior solutions.

## How to Search

```bash
# Search for a topic across all transcripts
grep -ril "<topic>" .agent-shell/transcripts/

# Show surrounding context
grep -n "<topic>" .agent-shell/transcripts/*.md

# List transcripts by date (most recent last)
ls .agent-shell/transcripts/

# Search for a specific decision or pattern
grep -A5 -B5 "<keyword>" .agent-shell/transcripts/*.md
```

## Notes

- The `.agent-shell/` directory lives at the project root (your working directory when the session started).
- Transcripts are named by date/time: `YYYY-MM-DD-HH-MM-SS.md`.
- Each transcript records the agent, working directory, and full conversation.
- Only search `.agent-shell/transcripts/` — do not modify these files.
