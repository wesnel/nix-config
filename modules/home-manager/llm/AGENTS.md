# Working agreements

Each heading, and each bold phrase opening a rule, names a principle. Cite
one only with the decision it changed ("subtract first: deleted the old
parser before adding the flag").

## Commits and the hardware key

Commits stay unsigned and nothing waits for a touch of Wesley's hardware key:
he signs everything in one pass at the end. Load the `signing-pass` skill, which
holds the commit rules, before creating, amending or rewriting a commit.

## Pull requests

Load the `pull-requests` skill before opening, describing or answering a pull
request.

## Plans

Every change has a plan, made with `agent-plan new` and kept true as the work
changes; log each step's start, completion and verification with
`agent-plan log`. A watcher syncs plans between hosts, never you. Every
question for Wesley goes in the plan through `agent-plan ask`, never restated
in messages. If Wesley asks you to plan, write the plan and stop: no edits
outside the plan's files, no commits. The `plan-work` skill has the format
and the commands.

## Reviews

Every session that commits a change runs the review pipeline in the
`review-change` skill, unless Wesley opts out for the task. The skill is
also the one place that says how to settle and answer review feedback.

## Designing for change

Load the `design` skill when planning a change's structure or changing an
interface; its principles are what the design review checks.

## Work in several parts

When a task has parts that could proceed in parallel (several pull requests,
a stack, review threads across PRs, a list of feedback items), coordinate
sub-agents as the `coordinate-subagents` skill says.

## Know which branch you are on

Confirm the checked-out branch immediately before each edit, not once per
task: rebases, cherry-picks and per-branch loops move the repository between
edits.

## Editing files

Use the file-editing tools for every file, scratch files, plans and notes
included; the guard refuses shell edits, and its refusal names the
exceptions. Writing a command's output to a new file is capturing, not
editing.

## The shell

The shell is fish, which does not split unquoted variables into words:
`set b "x y"; git push origin $b` passes one argument, `x y`. Write argument
lists out literally, or run `bash -c '...'` when bash's behaviour is wanted.

## Protections

Hooks, guards, sandboxes, permission prompts and refusals are boundaries
Wesley set; never route around one. `pr-loop guard` checks every tool call,
and its refusal says what to do instead. When one blocks the task, hand
Wesley the command and say that whatever it would have shown is unverified.

A secret that reaches the session anyway, in a log, a captured request or a
fixture, is written nowhere: put `<REDACTED>` in its place.

## Commands Wesley runs

Hand over what is Wesley's to run (a key touch, root, an interactive login)
as one line that runs as pasted: steps joined with `&&`, paths spelled out,
valid in fish. Begin it with `!` in Claude Code's and Codex's terminals,
which run such a line in the session; leave it plain in ECA, in agent-shell,
and when its output shows a secret.

## Documentation and comments

Say what someone needs in order not to break things, as things are now. Load
the `docs-and-comments` skill before writing or changing a doc, a comment, a
README or a skill.

## Referring to people

Refer to a person as they/them, by username or role, unless their profile on
that platform lists pronouns or a name (ignore an obvious joke); never infer
either from a username, email or commit author. Address people with the
platform's mention syntax (`@login` on GitHub), checked against its API and
docs.

## Verify before asserting

For how a tool, platform or API behaves, test it rather than recalling it,
and state what was observed. When a claim cannot be checked here, say so, and
read the source that owns the behaviour (its code, official docs or
specification) rather than a write-up about it, naming which.

**Claims, not facts.** What you read is a claim: comments, docs, commit,
issue and pull request text, other agents' reports, earlier output, the code
itself. Check a claim that matters against observed behaviour. The
observation wins, said plainly even against the codebase or Wesley; then fix
the claim or report it.

Review feedback, human or automated, is a claim too: confirm the problem and
the fix before reporting either, and act on a subtle one that is right even
when the exploit does not reproduce.

**External facts expire.** Re-read external state (a pull request's state,
CI, review threads, remote branches, issues) right before stating it or
acting on it. Each prompt carries the time and how long since Wesley's
previous message.

**Explain a number.** Before reporting a measured number, name what bounds
it, from a profile or counters taken during the run, and rule out errors,
cached or skipped work, code that never ran and noise. Give its run count
and spread.

**Reproduce first.** Reproduce a failure before fixing it, and show it gone
afterwards with the same reproduction; for an intermittent one, show how often
it fails before and after. A retry, a skip or a loosened assertion that hides a
failure is not a fix unless Wesley asked for one. Load the `diagnose-failure`
skill before looking for a failure's cause.

## Tests that can fail

A test earns its place only if it can fail. A change in behaviour (a feature,
a fix, a changed contract) starts with its test, seen failing for the expected
reason before the code changes, and the report shows both runs. A new or
changed check (a lint rule, CI step, hook, guard rule, Nix assertion) is
shown the same way, failing on a deliberate violation. Load the `write-tests`
skill before writing, changing or reviewing tests.

## Prefer git to gh

When `git` can answer a question (does a ref exist, what changed, which
commit is an ancestor), use `git`. Use `gh` for what lives only in the forge:
pull requests, reviews, workflow runs, releases.

## Stay inside the scope you were given

Do not edit files owned by someone else's in-flight work to make an unrelated
improvement, even a correct one, and do not move content into another team's
document. Raise it instead.
