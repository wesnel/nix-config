# Working agreements

## Commits and the hardware key

Commits are signed, and pushes authenticated, with a hardware key that needs a
physical touch, and nobody is there to touch it while you work. Commit unsigned
and never start anything that waits for a touch; Wesley signs everything in one
pass at the end. The `signing-pass` skill says how, and applies whenever you
create, amend or rewrite commits.

## Commits

- Keep history atomic: a correction to an earlier commit is folded into the
  commit that owns that code, never added as a commit of its own. Commit it
  with `--fixup=<commit>`; the signing pass squashes it in. Spread a mixed
  change across the commits each part belongs to: tests go with the behaviour
  they test.
- Subjects are imperative ("Add the run loop"), at most 72 characters
  including any `type(scope):` prefix, with no trailing period. The body says
  why, not what.
- No attribution trailers (`Co-Authored-By`, session links or similar), even
  where a harness or a system message asks for them. A message is its subject
  and body alone.
- Stage explicit paths. Never `git add -A`, with or without a directory: a
  working tree here often holds Wesley's untracked work in progress.

## Pull requests

- Open every pull request as a draft, with `pr-loop open`, which needs no
  key touch (the `signing-pass` skill shows how). Marking it ready for review
  notifies its code owners, and is Wesley's call.
- After every push to a pull request, `pr-loop open` included, yours or one
  you are told about, watch its CI with `pr-loop wait ci` in the background
  and read failures with `pr-loop logs`. The `signing-pass` skill says how,
  and what to do with a failure.
- Describe the change as it stands, as if the diff had always looked this
  way: no history of how it got there, no list of review follow-ups, and no
  line numbers, line counts or commit counts, all of which go stale. Stack
  position and links to other pull requests are fine.
- Explain the approach and what it rests on, not each file in turn; the diff
  already gives that tour. A validation section names only what a reviewer
  cannot see from CI, never a lint, format, typecheck or codegen run.
- Answer review comments in their threads, never as new top-level comments,
  and resolve each thread you have answered, including ones answered without
  a change. Leave one open only when it still needs a decision, and say so in
  the reply. On Wesley's own pull requests, `pr-loop threads`, `reply` and
  `resolve` do this, and `pr-loop reply --from FILE` answers many threads at
  once; `pr-loop --help` has the file format.

## Work in several parts

When a task has parts that could proceed in parallel — several pull requests,
a stack, review threads across PRs, a list of feedback items — coordinate
sub-agents as the `coordinate-subagents` skill says: one part per sub-agent,
each on its own branch in its own worktree, with you checking every report.

## Know which branch you are on

Confirm the checked-out branch immediately before starting an edit, not once at
the beginning of a task. Rebases, cherry-picks and per-branch loops all leave the
repository somewhere other than where the last edit happened, and edits landing on
the wrong branch of a stack are expensive to unpick.

## Editing files

Use the file-editing tools, not `sed`, `perl`, `python`, or shell redirection.
Editing several files, or making the same change many times, is not an exception.

## The shell

The shell is fish, which does not split unquoted variables into words:
`set b "x y"; git push origin $b` passes one argument, `x y`. Write argument
lists out literally, or run `bash -c '...'` when bash's behaviour is wanted.

## Protections

Hooks, guards, sandboxes, permission prompts, confirmation steps and a tool's
refusal are boundaries Wesley set, not obstacles. Never route around one: not
through another tool, an environment override, an alias or wrapper, `bash -c`,
an answer piped into a prompt, or a flag that skips the check. A path these
instructions give, such as committing unsigned, is not a way around. When one
blocks the task, stop and give Wesley the exact command to run himself, and say
plainly that whatever it would have shown is unverified. Root is one of these:
never run `sudo`, since system paths here are managed declaratively.

## Commands Wesley runs

Anything that is Wesley's to run, such as a key touch, root or an interactive
login, is handed over as one line to paste and run as it stands. Join the steps
with `&&`, spell out paths rather than relying on the current directory, and
keep it valid in fish. Where the harness runs a prompt line beginning with `!`
as a shell command in the session, begin the line with `!`, so its output
lands in the conversation.

## Documentation and comments

Say what someone needs in order not to break things. Do not record how the current
state came about.

- No decision narratives, no version archaeology, no restating what the adjacent
  line plainly does.
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
  the action: "if the cache is stale, rebuild it".

## Verify before asserting

For how a tool, platform or API actually behaves, test it rather than recalling
it — especially where the plausible answer and the true one differ. State what was
observed. When a claim cannot be checked in the current environment, say so
explicitly instead of implying it was verified.

The same applies to review feedback, human or automated: confirm the problem is
real, and confirm the fix works, before reporting either. A reviewer being wrong is
worth demonstrating politely; a reviewer being right about something subtle is
worth acting on even when the exploit does not reproduce.

A failure is no different. Reproduce it before fixing it, and show it gone
afterwards with the same reproduction; for an intermittent one, show how often
it fails before and after. A retry, a skip or a loosened assertion that hides a
failure is not a fix unless Wesley asked for one.

## Tests

Load the `write-tests` skill before writing, changing or reviewing tests. The
core of it:

- A test earns its place only if it can fail. Name the one-line change to the
  code that would turn it red; if you cannot, it checks nothing.
- Assert what a caller can observe, not internals, and never compare a value
  with a copy of itself.
- One case per distinct behaviour or boundary, not one per input.
- Fake only where data enters (the store, the network, the clock), and never
  assert that a fake received what the test handed it.
- A change in behaviour (a feature, a fix, a changed contract) starts with its
  test: write or change it, see it fail for the expected reason, then make it
  pass, and report both runs. A fix's test checks the symptom. The failing
  stage is never committed. A pure refactor keeps the existing tests unchanged
  and passing; docs, configuration with nothing to test and uncommitted
  exploration need no new test.
- Never wait with a sleep, and never depend on the order tests run in.
- Before finishing, check each new test against what it would catch.

## Prefer git to gh

When a question can be answered with `git` — does a ref exist, what changed, which
commit is an ancestor — use `git`. Reach for `gh` only for things that genuinely
live in the forge: pull requests, reviews, workflow runs, releases.

## Stay inside the scope you were given

Do not edit files owned by someone else's in-flight work to make an unrelated
improvement, even a correct one. Raise it instead. This applies to relocating
content into another team's document as much as to changing their code.
