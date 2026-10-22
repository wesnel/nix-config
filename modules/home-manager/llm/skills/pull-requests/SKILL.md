---
name: pull-requests
description: How to open, describe and answer a pull request. Use before `pr-loop open`, `gh pr create` or `gh pr edit`, before writing a pull request's title or description, and before replying in its review threads.
---

# Pull requests

## Opening

- Open every pull request as a draft. Marking it ready for review notifies
  its code owners, and is the operator's call.
- Open one for a branch GitHub does not have yet with `pr-loop open`, which
  pushes the unsigned commits through the GitHub API, needs no key touch,
  and opens the pull request as a draft:

  ```sh
  pr-loop open --title "Say what changed" --body-file description.md feature
  ```

  The base is the default branch, or with `--base` the branch of an open
  pull request of the operator's that this one stacks on, whose GitHub stack
  open adds it to; when it cannot, `unstacked` in its output says why and
  which `pr-loop link` stacks it later. It refuses a branch GitHub already
  has, and a branch that contains a remote branch its base lacks; later commits reach GitHub through the `signing-pass` skill's pass.
- After every push to a pull request, watch its CI as the `signing-pass`
  skill's "After a push" says.

## The description

- Describe the change as it stands, as if the diff had always looked this
  way: no history of how it got there, no list of review follow-ups, and no
  line numbers, line counts or commit counts, all of which go stale. Stack
  position and links to other pull requests are fine.
- Explain the approach and what it rests on, not each file in turn; the diff
  already gives that tour. A validation section names only what a reviewer
  cannot see from CI, never a lint, format, typecheck or codegen run.
- Say whether the change is easy to back out (a two-way door) or not (a
  one-way door: a migration, a stored format, a published interface), and
  what it could break if it is wrong.
- Write it as the `docs-and-comments` skill's "Write plainly" says.

## Review threads

Answer review comments in their threads, never as new top-level comments.
On the operator's own pull requests, `pr-loop threads`, `reply` and `resolve`
do this for review bots' threads. The `review-change` skill says how to
settle each one.

## People

Before naming or referring to a reviewer, author or code owner, read the
name and pronouns their GitHub profile lists, which only the GraphQL API
returns:

```sh
gh api graphql -f query='query{user(login:"LOGIN"){name pronouns}}'
```

A null field means none is listed: use their `@LOGIN` and they/them, as
AGENTS.md's "Referring to people" says.
