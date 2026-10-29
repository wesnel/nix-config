---
name: coordinate-subagents
description: Split work with several parts across sub-agents, each doing one part on its own branch in its own git worktree, while you coordinate, decide what spans parts, check every report and hand back to the operator. Use when a task has several parts that could proceed in parallel, such as several pull requests or a stack of them, open review threads across PRs, a list of feedback items, or several independent changes.
---

# Coordinating sub-agents

You coordinate. One sub-agent per part does that part's work on its own
branch, in its own worktree. You own the split, the order and base of every
stacked branch, the decisions that span parts, the check of every report,
and coalescing every branch into one signing pass that the operator runs.
Nothing reaches GitHub until they have signed and pushed.

The work as a whole goes through the `review-change` skill's pipeline, as
any change does. This skill adds what several branches need: the design
review across all parts (Design review below) and a fresh review of each
branch (Review each branch).

A review loop over a stack, which the operator starts with
`review-loop`, has its own skill, `review-loop`.

## Where you run

- Coordinate from the main checkout, never from a session that has entered a
  worktree (Claude Code's `EnterWorktree`). Its sub-agents inherit that
  worktree as the only place they may write, and cannot write in their own.
- Leave the main checkout on its branch and its files alone: it holds the
  operator's untracked work. Read another branch with
  `git show <branch>:<path>` or in that branch's worktree.

## Split the work

- Before splitting, write the plan's Distribution with `agent-plan part`,
  as the `plan-work` skill says: one part (`P1`, `P2`…) per part, with its
  repository, branch, base, worktree and what it does. A part that needs
  another's work first names that part with `--after`, or later with
  `agent-plan after`.
- One part is one branch, one worktree and one sub-agent.
- Order the parts, and the commits within each, as the `verifiable-steps`
  skill says.
- Before giving parts state to write that another part also writes, load
  the `separate-before-serializing` skill.
- Before giving several parts the same kind of work, load the
  `build-the-lever` skill.
- An interface change and the move of the callers we control are one part,
  never split across branches, as the `migrate-and-delete` skill says.
- Each branch of a stack builds and passes its tests on its own base, so it
  can merge before the branches above it.
- Parts that edit the same file or the same code are never parallel
  branches: that only produces conflicts. Give them to one sub-agent as one
  stacked chain, or run them one after another.
- Coupled pull requests, such as a stack or a spec and its implementation, go
  to one sub-agent. They stay separate branches with separate commits.
- In a stack you decide each branch's base and the order of the work. Before
  a parent branch changes, record each child's fork point with
  `git merge-base <child> <parent>`; the restack needs it (the
  `restack-branches` skill).
- Create each worktree yourself, outside the main checkout, on the branch the
  part belongs to, and name both in the task. Base new independent work on
  `origin/<default>`, using the remote-tracking ref already available. For
  a local or stacked base, check that it contains that ref first:

  ```sh
  git merge-base --is-ancestor origin/<default> <base>
  ```

  Exit 1 means the base lacks upstream commits: update the base or restack
  before building on it. If the remote-tracking ref needs fetching with the
  operator's key, hand over that fetch rather than silently using local
  `main`. Create a new branch with `--no-track`, so it does not track
  `origin/<default>`, which the signing pass refuses to push to:

  ```sh
  git worktree add --no-track <worktrees>/<repo>/<branch> -b <branch> <base>   # new work
  git worktree add <worktrees>/<repo>/<branch> <branch>             # existing branch
  ```

  A harness's own worktree option (Claude Code's `isolation: "worktree"`)
  invents the branch name, so use it only for read-only investigation.
- In Claude Code and Codex, once the harness gives a sub-agent its ID, make
  it the worktree's owner, and the guard denies every other agent's edits
  there, yours included (`main`); hand a worktree back to yourself the same
  way when the part is done:

  ```sh
  agent-plan own PLAN --worktree <worktrees>/<repo>/<branch> --agent <agent ID>
  ```

  Run it before the part starts writing: the guard refuses a sub-agent's
  edits outside the worktrees it owns, scratch and the plans directory, all
  of them until it owns one.

## Agent types

In Claude Code, start each sub-agent as the type for its role, the Agent
tool's `subagent_type`. Each definition carries its role's method, report,
tools and model, and the rules every sub-agent follows; a task adds only
what is this work's.

- **`builder`:** a part's branch, from its task to a verified branch ready
  for review; its fix rounds too, continued with `SendMessage`; and a
  part's Investigate phase (Phases below), which commits nothing.
- **`adversarial-reviewer`:** tries to break a finished branch, every
  finding reproduced or marked as not, with what held: your review of each
  part (Review each branch below); also the second opinion where
  `git delegate` cannot run the selected provider, from the prompt
  `git delegate --print-prompt` prints, replying in the form it gives.
- **`design-reviewer`:** the design reviews (Design review below).
- **`security-reviewer`**, **`compat-reviewer`** and
  **`upgrade-reviewer`:** that focus's review of a part's range outside
  the fresh review loop, or from the prompt
  `git delegate --print-prompt --reviewer ROUTE` prints where delegate
  cannot run it headless (its stderr names the type). An
  `upgrade-reviewer` reads only the upstream text its prompt holds: give
  it that prompt, never a task to look upstream changes up itself.
- **`Explore`:** read-only lookups and inventories.

The reviewers have no editing tools. The guard lets them run git's
reading commands, and any git command in a repository of their own under
scratch, and refuses the writes it can see outside scratch; git's
configuration can still make a read run a program, and a script or a
program the guard does not know still writes, so a reviewer's task never
asks for either.

Codex has no such types: there the rules file also names the installed
working agreements to read (`~/.codex/AGENTS.md`) and carries the role's
rules, from its definition in `~/.claude/agents` where Claude Code is
installed.

## The rules file

Write what every part of this work shares once, to a file in your
scratchpad, and start every task with "First read <file> and follow it
exactly." Put in it:

- How to build, test and lint in this repository, which commands need the
  sandbox off, and what a fresh worktree lacks (generated files, for
  example).
- Where thread replies are drafted: `<scratchpad>/replies/pr-<N>.md`.
- No wrapper shell scripts.

When several parts need the same exploration, start one sub-agent that only
reads to do it first and write what it found to a notes file in your
scratchpad, and name that file in the rules.

## Each task

The task names the rules file; the plan and the part's ID; the repository,
branch, base and worktree; the part's goal and what is not its job; the
decisions already made, as final; and, where you know it, the commit each
correction folds into. It says what the part must do, not how, as
acceptance criteria each checkable on its own, and gives the report a
length.

## Phases

Work that needs the operator's decisions, such as review threads or a
feedback list, runs in phases.

1. **Investigate.** Nothing is committed. Each sub-agent checks every item
   against the code and tries to reproduce it, and gives a verdict with its
   evidence: real, not real, partly real, or cannot verify here. It proposes a
   fix and the commit the fix folds into, and may try the fix in its worktree,
   leaving it uncommitted.
2. **Decide.** Merge the reports into one list for the operator: a question
   per decision through `agent-plan ask`, with its options and your
   recommendation.
3. **Fix.** One sub-agent per part, or per coupled stack, with the operator's
   decisions written into its task. It ports any experiment from its
   investigation worktree, committing none of the scratch tests as they are.
4. **Publish**, once the operator's signing pass has pushed. Re-read each
   thread, drop one already resolved or answered from its reply file,
   re-check each drafted reply against the pushed code, then answer each
   thread as the `review-change` skill's Answering says. Update each
   description to the change as it stands.

## Design review

Run the `review-change` skill's design review at two points, over every
part at once:

- before splitting: settle the structure, find the files parts share, and
  decide which branches stack;
- before Coalesce, once a batch of sub-agents has finished: cohesion,
  duplication, contradictions, the same file edited on parallel branches,
  and tooling that has drifted apart.

Settle its questions as the `plan-work` skill's "Questions for the operator"
says: decide the reversible ones yourself, and merge the one-way doors and
product direction into the Decide phase's list when there is one. A design
on which sub-agents, or you and the design review, disagree gets a second
opinion with `git delegate ask` first, as the `review-change` skill's
design review says.

A finding by one sub-agent that bears on another part goes to that part's
sub-agent, in its current task or its next one.

## Model and effort

Start every sub-agent with the model and effort this section gives; it is
what permits setting them. In Claude Code they are the Agent tool's `model`
and `effort`, which override the agent type's own: Opus at high effort for
every type above but `Explore`. Cheaper work is the same
type at the model and effort the list below gives. Codex's `spawn_agent`
takes a `model`. Start an `adversarial-reviewer` with `effort` and no
`model`: its definition names its model, which through a gateway is of
another family than the author's.

- **Haiku 5.5, low effort:** read-only lookups, inventories, running a given
  command and reporting it.
- **Sonnet 5.5, medium effort:** well-specified changes with tests and
  little judgement, such as a flag from a precise brief, wording edits, or a
  restack with few conflicts.
- **Opus 5.5, the default:** design reviews, adversarial reviews,
  security-sensitive code (the guard, the sandbox, `git sign-pass`,
  `pr-loop`), restacks with many conflicts, and anything ambiguous.
- Reviewers are never given a cheaper model: the second opinion is the
  other model family, as the `review-change` skill's Harness choice says.

Lower the effort before the model: routine work runs on Opus at low or
medium effort before it moves to Sonnet or Haiku. Prompt caches belong to
one model, so mixing models loses their reuse; judge the cost of a completed
task, not of a request. When a cheaper sub-agent's report fails Check every
report, or a review of its work finds real defects, rerun the task on Opus.

Keep briefs and reports small: a task points at files, such as the rules
file and the plan, rather than pasting them, and gives the report a length.

## Running them

Start independent sub-agents together, in the background, and each part as
soon as `agent-plan status PLAN` lists it ready to start. Steps you run
yourself follow the same list: "Steps ready" holds the open steps that
wait on no `:AFTER:` item, follow every earlier sibling under an
`:ORDERED: t` parent, and are not scheduled for later.

- **Claude Code:** the Agent tool with `run_in_background`. `SendMessage`
  continues an agent with its context.
- **Codex:** `spawn_agent`. `send_input` continues an agent, and `wait_agent`
  collects results.
- **No sub-agents:** do each part yourself, one at a time, in its worktree.

Run at most three sub-agents at once, and one heavy job across all of
them: a Nix build, a VM, `git red-check`, or a full `go test ./...` of
agent-tools: more than that exhausts the machine's memory. Before starting
another, check memory pressure and wait while it is high: on macOS,
`sysctl kern.memorystatus_vm_pressure_level` above 1 (2 is warn, 4
critical); on Linux, `some avg10` above 10 in `/proc/pressure/memory`.
Tell each sub-agent which heavy job it may run, and when.

Never edit a sub-agent's worktree while it runs. To learn where something
belongs in its part, ask it.

Every turn of yours re-sends your whole context, so spend turns on
decisions:

- Each sub-agent reports once, at the end, and waits in the foreground for
  its own builds and tests, so it stops only when its work is done: the
  role agents' definitions say so, and elsewhere the rules file does.
- Answer a notification only to decide something or to tell an agent
  something. Check reports that arrive together in one pass.
- Run a fix round by continuing the agent that did the work (`SendMessage`,
  `send_input`), whose context already holds it.
- Over the plan's usage limits, wait longer between checks, in fewer turns.

**The quota stop.** In Claude Code, once a quota window reaches the
threshold set in `wgn.home.claude.quotaStop`, every session is refused
every call but reading, asking the operator, `TaskStop`, `SubagentHandback`,
`agent-plan` alone on its line, and a resume scheduled for after the reset,
until the window resets; the refusal names the window, its share and its
reset time, and gives the main session the exact `ScheduleWakeup` or
`CronCreate` call that schedules its resume. Each running sub-agent meets
the same stop and hands back. Start and continue no one; log each report
as it arrives, make that call, then hand back to the operator with the
window, its reset time and the parts still open. The operator alone lifts
the stop.

In Codex the same stop holds at `wgn.home.codex.quotaStop`, from the Codex
account's windows, and lets through only `update_plan`,
`request_user_input`, `wait_agent`, `close_agent` and `agent-plan` alone on
its line. Codex cannot schedule a resume: log each report as it arrives,
then end your turn with the window, its reset time and the parts still
open, for the operator to resume you after the reset.

## Check every report

Before relaying a report, confirm it:

- `git -C <worktree> branch --show-current` is the branch, and
  `git -C <worktree> status --short` shows nothing but ignored files.
- `git -C <worktree> log --format='%h %G? %s%n%b' <base>..<branch>` matches
  the report: every commit `N`, no trailers but a fixup's `Review-*` ones,
  subjects in shape, on the base you named. Read the commits that matter
  with `git show`.
- `agent-plan status PLAN` shows each of the part's steps done and
  verified, none of them under keyword drift, and each done step's commit
  subject is in that log.
- The specific tests it cites pass when you run them; whole suites are left
  to CI and the part's own `git red-check` run.

Accepting a report is logging it, never editing the plan's text: once a
report passes, log `done` for its part's ID, which marks its heading
`DONE` and readies the parts that wait for it, then `verified` with the
checks you ran as evidence, and `handed-back` once the operator has the
branch. Relay only what you confirmed, and say what you did not. Each
question it raises goes through `agent-plan ask`, as the `plan-work`
skill's "Questions for the operator" says, not into your message.

## Review each branch

Before Coalesce, run the `review-change` skill's fresh review loop on each
branch by itself, never the whole stack at once: the range `<base>..<branch>`,
the part's Goal as the goal statement, at most three rounds. Your general
review of a part is an `adversarial-reviewer` sub-agent given that range,
plan and part, in place of `/code-review`; `git delegate --plan PLAN --part
P1` stays the second opinion. Review a branch once its
sub-agent has finished it, then hand all of that round's findings to the
sub-agent as one fix round, to settle as Triage says; check its report as
any other.

## Coalesce

The branches go to the operator as one piece of work. Before handing back, make
them fit together:

- Settle the order of every stack and the base of every branch.
- For each pair of branches that touch the same files,
  `git merge-tree --write-tree <a> <b>` must exit 0. On a conflict, resolve
  it or restack one onto the other as the `restack-branches` skill says.
- Every `fixup!` commit names a commit in `<base>..<branch>` of its own
  branch.
- After any restack, rerun that branch's checks, and those of every branch
  stacked on it.
- For each branch, `git rev-parse --abbrev-ref <branch>@{upstream}` is the
  branch's own name on the remote, or fails because the branch is new. Know
  which are new.

## Hand back

- For every branch, bottom of each stack first,
  `git log --format='%h %G? %s' <base>..<branch>`.
- Beside it, the signing pass, one command per repository naming the top
  of every stack, each child of a fork included, handed over as the
  `signing-pass` skill's Handing over says.
- The worktrees stay until the pass is done.
- Everything that needs the operator's decision, and everything unverified.

## After the push

If you are still running once the operator has pushed:

- Open a pull request for each new branch as the `pull-requests` skill
  says, based on its parent branch in a stack, then make each stack one
  GitHub stack, which GitHub shows on
  every pull request in it and merges from the bottom:
  `pr-loop link <bottom> ... <top>`, by number or branch. It refuses bases
  that do not chain, and leaves a stack already linked as it is.
  `pr-loop stack <pr>` shows GitHub's stack under `github`.
- Run the **Publish** phase: the drafted replies, and every description
  brought up to the change as it stands.
- Watch every pull request's CI as the `signing-pass` skill's "After a push"
  says; a fix is coalesced and handed back the same way.
- When nothing is left to fix, `git worktree remove` each worktree.
