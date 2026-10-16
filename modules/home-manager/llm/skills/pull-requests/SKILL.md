---
name: pull-requests
description: How to open, describe and answer a pull request. Use before `pr-loop open`, `gh pr create` or `gh pr edit`, before writing a pull request's title or description, and before replying in its review threads.
---

# Pull requests

## Opening

- Open every pull request as a draft, with `pr-loop open`, which needs no
  key touch (the `signing-pass` skill shows how). Marking it ready for review
  notifies its code owners, and is the operator's call.
- After every push to a pull request, watch its CI as the `signing-pass`
  skill's "After a push" says.

## The description

- Describe the change as it stands, as if the diff had always looked this
  way: no history of how it got there, no list of review follow-ups, and no
  line numbers, line counts or commit counts, all of which go stale. Stack
  position and links to other pull requests are fine.
- Explain the approach and what it rests on, not each file in turn; the diff
  already gives that tour.
- Put what you ran beyond CI under `## Verification beyond CI`, one claim
  per item: the command, where it ran, and what it showed. Name only what a
  reviewer cannot see from CI, never a lint, format, typecheck or codegen
  run. With nothing to claim, leave the heading out: "None." under it is
  audited as a claim. Before opening or editing, write the description to
  a file and run `cd WT && git fresh-review audit --claims FILE
  <base>..HEAD`, WT being the branch's worktree; take out each claim it
  finds unsupported, or run what shows it and audit again. Where the
  commands ran in an earlier session (before `/clear`), add `--session ID`
  for it and for this one; its ID names its logs, `ID.DAY.jsonl` in
  `$XDG_STATE_HOME/agent-tools/evidence`. `pr-loop open`, and `gh pr
  create` or `gh pr edit` with `--body-file`, refuse the section until an
  audit of the claims as they stand, for the change as it stands, supports
  every one. Run gh as `cd WT && gh pr edit --body-file FILE`, a command
  line of its own, or as `gh pr edit N --body-file FILE` from a checkout of
  the same repository; it is refused a description the guard cannot read
  (from stdin, an editor, a template, the browser or a shell expansion) and
  the section given inline. In a review loop or a Codex session, leave the
  section out and put the claims in your report: no audit can support
  them, since a loop's calls are in no log on this host and Codex's carry
  no exit status.
- Under `## Reverting`, say whether the change is easy to back out (a
  two-way door) or not (a one-way door: a migration, a stored format, a
  published interface), what it could break if it is wrong, and what
  reverting its commits leaves behind: data written in a new format,
  settings people made for it, state on other hosts. A compat review
  (the `review-change` skill's focused reviews) checks this section
  against the code, and refuses a description without it.
- Write it as the `docs-and-comments` skill's "Write plainly" says.

## Review threads

Answer review comments in their threads, never as new top-level comments.
On the operator's own pull requests, `pr-loop threads`, `reply` and `resolve` do
this. The `review-change` skill says how to settle each one.

## People

Before naming or referring to a reviewer, author or code owner, read the
name and pronouns their GitHub profile lists, which only the GraphQL API
returns:

```sh
gh api graphql -f query='query{user(login:"LOGIN"){name pronouns}}'
```

A null field means none is listed: use their `@LOGIN` and they/them, as
AGENTS.md's "Referring to people" says.
