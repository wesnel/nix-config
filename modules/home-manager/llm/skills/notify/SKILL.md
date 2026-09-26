---
name: notify
description: Push a desktop notification to Wesley via emacsclient. Use when an agent needs attention (task complete, blocked on input, review required).
---

# Notify Wesley via Emacs

Send a desktop notification to Wesley by invoking `emacsclient -e` from
the shell. Emacs dispatches through `wgn/notify`, which prefers OSC 777
escape sequences so the notification is rendered by the client terminal
(e.g. Ghostty over SSH), and falls back to a native GUI notification
when Emacs is running locally as a GUI.

Use this to get Wesley's attention when you would otherwise be blocking
silently — it is a push signal, not a polling one.

## When to Use

- A long-running task has completed and Wesley should look at the result.
- The agent is blocked and needs Wesley's input, decision, or credentials.
- Code, a plan, or a migration is ready for review.
- An unrecoverable error occurred and the agent is stopping.

Do **not** use for routine progress chatter. Every notification should
be worth interrupting Wesley for.

## Command

```
emacsclient -e '(wgn/notify "<title>" "<message>")'
```

- `<title>`: short, actionable summary (aim for ~40 characters).
- `<message>`: optional single-line detail.

Escape embedded double quotes with `\"`. Semicolons in the title or
message are auto-replaced with commas because they would break the OSC
escape.

## Examples

```bash
emacsclient -e '(wgn/notify "Tests passed" "342 tests green in 12s")'
emacsclient -e '(wgn/notify "Needs review" "Migration ready — approve or reject")'
emacsclient -e '(wgn/notify "Blocked" "Which backend: JWT or session cookies?")'
emacsclient -e '(wgn/notify "Deploy failed" "See build 4821 for the stack trace")'
```

## Notes

- Fire-and-forget: the call returns immediately.
- If no Emacs server is running, `emacsclient` will error. That usually
  means Wesley is not actively using Emacs — silently move on rather
  than retrying.
- The notification title/message are the only signal Wesley receives.
  Make the title specific enough to act on without opening the chat.
