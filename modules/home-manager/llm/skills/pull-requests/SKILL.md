---
name: pull-requests
description: How to open, describe and answer a pull request. Use before `pr-loop open`, `gh pr create` or `gh pr edit`, before writing a pull request's title or description, and before replying in its review threads.
---

# Pull requests

## Opening

- Open every pull request as a draft. Marking it ready for review notifies
  its code owners, and is the operator's call.
- `pr-loop next <branch>` says whether a branch's next step is opening its
  pull request, the operator's signing pass or push, or nothing; take the
  step it names.
- Open one with `pr-loop open`, which pushes a branch GitHub does not have
  yet through the GitHub API, unsigned, or takes one GitHub already has at
  the local tip with signatures verified against the operator's protected
  global signing identity, and opens the pull request as a draft:

  ```sh
  pr-loop open --title "Say what changed" --body-file description.md feature
  ```

  The base is the default branch, or with `--base` the branch of an open
  pull request of the operator's that this one stacks on, whose GitHub stack
  open adds it to; when it cannot, `unstacked` in its output says why and
  which `pr-loop link` stacks it later. It refuses a branch that already
  heads an open pull request, a branch GitHub has at another commit or with
  commits whose signatures do not prove that identity, and a branch that contains a remote
  branch its base lacks; later commits reach GitHub through the
  `signing-pass` skill's pass.
- After every push to a pull request, watch its CI as the `signing-pass`
  skill's "After a push" says.

## The description

- Describe the change as it stands, as if the diff had always looked this
  way: no history of how it got there, no list of review follow-ups, and no
  line numbers, line counts or commit counts, all of which go stale. Stack
  position and links to other pull requests are fine.
- Explain the approach, not each file in turn; the diff already gives that
  tour. A validation section names only what was checked that a reviewer
  cannot see from CI, never a lint, format, typecheck or codegen run.
- Under `## Weak spots, assumptions and decisions`, point reviewers, and
  the review bots that read the description, at what deserves their doubt,
  one line per item; a small change may have a single item:
  - the weakest places in the change: a rough edge, the part hardest to get
    right, the code you are least sure of, each by file or function, with
    why;
  - the assumptions it rests on, about callers, data, the environment or
    another system's behaviour, each with what goes wrong if it does not
    hold;
  - each decision this pull request's code rests on, from the questions
    its plan settled, as the choice and an alternative the question
    recorded, so a reviewer can argue for that alternative.

  Write each as the change stands: an alternative is one not taken, never a
  step tried, and an item that review settled leaves the section or is
  restated as it now stands. What backing the change out does goes under
  `## Reverting`, and what was checked goes in the validation section.
- Under `## Reverting`, say whether the change is easy to back out (a
  two-way door) or not (a one-way door: a migration, a stored format, a
  published interface), what it could break if it is wrong, and what
  reverting its commits leaves behind: data written in a new format,
  settings people made for it, state on other hosts. A compat review
  (the `review-change` skill's focused reviews) checks this section
  against the code, and refuses a description without it.
- Write it as the `docs-and-comments` skill's "Write plainly" says.
- Write for a reader who has never seen the tools that made the change:
  link a review thread by its URL, a commit by its short SHA and a ticket by
  its key, and keep plan IDs, loop terms, local paths and session IDs to
  yourself. `pr-loop`, the guard and the signing pass refuse text naming
  them, commit messages included.

## Review threads

Answer review comments in their threads, never as new top-level comments.
On the operator's own pull requests, `pr-loop threads` reads them, and the
`review-change` skill says how to settle each one and who posts its answer.
A reply is written for the same reader as the description.

## People

Before naming or referring to a reviewer, author or code owner, read the
name and pronouns their GitHub profile lists, which only the GraphQL API
returns:

```sh
gh api graphql -f query='query{user(login:"LOGIN"){name pronouns}}'
```

In Codex, the `agent_ops` server's `github_profile` tool reads them.
A null field means none is listed: use their `@LOGIN` and they/them, as
AGENTS.md's "Referring to people" says.
