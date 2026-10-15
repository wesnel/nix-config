# Working agreements

Each heading below, and each bold phrase that opens a rule, names a
principle. Cite a name in a plan, report or review together with the
decision it changed ("subtract first: deleted the old parser before adding
the flag"), and only then.

## Commits and the hardware key

Commits stay unsigned and nothing waits for a touch of Wesley's hardware key:
he signs everything in one pass at the end. The `signing-pass` skill holds the
commit rules and the hand-over; load it before creating, amending or
rewriting a commit.

## Pull requests

Load the `pull-requests` skill before opening, describing or answering a pull
request.

## Plans

Every change has a plan, made with `agent-plan new` and kept true as the work
changes. Log each step's start, completion and verification with
`agent-plan log`, and never edit a plan's log; a watcher syncs plans between
hosts, never you. Every question for Wesley, wherever these agreements or a
skill call for one, goes in the plan through `agent-plan ask`, and is never
restated in messages. If Wesley asks you to plan,
write the plan and stop: no edits outside the plan's files, no commits. The `plan-work` skill has the format and the commands.

## Reviews

Every session that commits a change runs the review pipeline in the
`review-change` skill, unless Wesley opts out for the task. The skill is
also the one place that says how to settle and answer review feedback.

## Designing for change

Load the `design` skill when planning a change's structure or changing an
interface; its principles are what the design review checks.

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

Use the file-editing tools; the guard refuses shell edits, and its refusal
names the exceptions.

## The shell

The shell is fish, which does not split unquoted variables into words:
`set b "x y"; git push origin $b` passes one argument, `x y`. Write argument
lists out literally, or run `bash -c '...'` when bash's behaviour is wanted.

## Protections

Hooks, guards, sandboxes, permission prompts and refusals are boundaries
Wesley set. `pr-loop guard` checks every tool call, and its refusal says what
to do instead: do that, and never route around one by another path. When
one blocks the task, hand Wesley the command as "Commands Wesley runs" says,
and say plainly that whatever it would have shown is unverified.

A secret that reaches the session anyway, in a log, a captured request or a
fixture, is written nowhere: put `<REDACTED>` in its place in reports, plans,
pull requests and replies.

## Commands Wesley runs

Anything that is Wesley's to run, such as a key touch, root or an interactive
login, is handed over as one line to paste and run as it stands. Join the steps
with `&&`, spell out paths rather than relying on the current directory, and
keep it valid in fish. In Claude Code's and Codex's terminal interfaces, which
run a prompt line beginning with `!` as a shell command in the session, begin
the line with `!` so its output lands in the conversation; ECA and Claude Code
through agent-shell do not, so there the line is plain, for `M-!` or a Ghostel
terminal. A line whose output shows a secret is always plain. A `git sign-pass`
line carries `--notify ID`, which brings its result back to the session
however Wesley runs it; the signing-pass skill says how.

## Documentation and comments

Say what someone needs in order not to break things, as things are now. Load
the `docs-and-comments` skill before writing or changing a doc, a comment, a
README or a skill.

## Verify before asserting

For how a tool, platform or API actually behaves, test it rather than recalling
it — especially where the plausible answer and the true one differ. State what was
observed. When a claim cannot be checked in the current environment, say so
explicitly instead of implying it was verified, and read the source that owns
the behaviour (its code, its official docs, its specification) rather than a
write-up about it, naming which.

The same applies to review feedback, human or automated: confirm the problem is
real, and confirm the fix works, before reporting either. A reviewer being wrong is
worth demonstrating politely; a reviewer being right about something subtle is
worth acting on even when the exploit does not reproduce.

**Explain a number.** Before reporting a measured number (a speedup, a
latency, a count, a pass rate), name what bounds it, from a profile or
counters taken during the run rather than from reading code, and rule out
what else it could be measuring: errors, cached or skipped work, code that
never ran, noise. Report it with its run count and spread.

**Reproduce first.** Reproduce a failure before fixing it, and show it gone
afterwards with the same reproduction; for an intermittent one, show how often
it fails before and after. A retry, a skip or a loosened assertion that hides a
failure is not a fix unless Wesley asked for one. Load the `diagnose-failure`
skill before looking for a failure's cause. When a failure could recur, the
`retro` skill says how to propose what would prevent it.

## Tests that can fail

A test earns its place only if it can fail. A change in behaviour (a feature,
a fix, a changed contract) starts with its test, seen failing for the expected
reason before the code changes, and the report shows both runs. Test against
the real dependency wherever it can run inside the test; a fake is the
exception, and the test says why. Load the `write-tests` skill before writing,
changing or reviewing tests.

A new or changed check, such as a lint rule, CI step, hook, guard rule or Nix
assertion, is shown the same way: failing on a deliberate violation for the
expected reason, then passing, with both runs in the report.

## Prefer git to gh

When a question can be answered with `git` — does a ref exist, what changed, which
commit is an ancestor — use `git`. Reach for `gh` only for things that genuinely
live in the forge: pull requests, reviews, workflow runs, releases.

## Stay inside the scope you were given

Do not edit files owned by someone else's in-flight work to make an unrelated
improvement, even a correct one. Raise it instead. This applies to relocating
content into another team's document as much as to changing their code.
