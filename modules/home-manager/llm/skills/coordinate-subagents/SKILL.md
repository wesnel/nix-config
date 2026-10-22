---
name: coordinate-subagents
description: Split work with several parts across sub-agents, each doing one part on its own branch in its own git worktree, while you coordinate, decide what spans parts, check every report and hand back to Wesley. Use when a task has several parts that could proceed in parallel, such as several pull requests or a stack of them, open review threads across PRs, a list of feedback items, or several independent changes.
---

# Coordinating sub-agents

You coordinate. One sub-agent per part does that part's work on its own
branch, in its own worktree. You own the split, the order and base of every
stacked branch, the decisions that span parts, the check of every report,
and coalescing every branch into one signing pass that Wesley runs. Nothing
reaches GitHub until he has signed and pushed.

The work as a whole goes through the `review-change` skill's pipeline, as
any change does. This skill adds what several branches need: the design
review across all parts (Design review below) and a fresh review of each
branch (Review each branch).

A bot review loop over a stack has its own skill, `review-loop`, with the same
shape.

## Where you run

- Coordinate from the main checkout, never from a session that has entered a
  worktree (Claude Code's `EnterWorktree`). Its sub-agents inherit that
  worktree as the only place they may write, and cannot write in their own.
- Leave the main checkout on its branch and its files alone: it holds
  Wesley's untracked work. Read another branch with `git show <branch>:<path>`
  or in that branch's worktree.

## Split the work

- Before splitting, write the plan's Distribution, as the `plan-work`
  skill says: a part ID (`P1`, `P2`…) per part, with its branch, base,
  worktree and steps. A part that needs another's work first names that
  part in its `:AFTER:`.
- One part is one branch, one worktree and one sub-agent.
- A refactor the change needs goes first: the bottom branch of the stack,
  or the first commit of a single branch, with no test changed.
- A change to an interface with many callers goes expand–contract: add the
  new form beside the old, move the callers over in batches that each pass
  on their own, and remove the old form last.
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
  part belongs to, and name both in the task:

  ```sh
  git worktree add <worktrees>/<repo>/<branch> -b <branch> <base>   # new work
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

## The rules file

Write the rules once, to a file in your scratchpad, and start every task
with "First read <file> and follow it exactly." Do not count on a sub-agent
having loaded AGENTS.md or the skills you have: the file names them to read,
and adds only what they do not say. Put in it:

- Read and follow the installed working agreements (`~/.claude/CLAUDE.md`
  for Claude Code, `~/.codex/AGENTS.md` for Codex, `~/.config/eca/AGENTS.md`
  for ECA) and the `SKILL.md` of the `signing-pass`, `write-tests`,
  `review-change` and `plan-work` skills in the agent's skill directory.
- The plan's file. Each sub-agent makes its part's file with
  `agent-plan new --parent PLAN --part <its part ID>` and logs to the
  plan's log with `--role subagent`.
- Work only in the worktree the task names, never the main checkout or another
  worktree. Run `git branch --show-current` immediately before every edit and
  every commit. Never `git stash`: every worktree shares one stash.
- `trash` instead of `rm`, and no wrapper shell scripts.
- How to build, test and lint in this repository, which commands need the
  sandbox off, and what a fresh worktree lacks (generated files, for
  example).
- Before reporting done: a trial autosquash on a throwaway branch, deleted
  afterwards, with the tests and linters run on the squashed result, since a
  squash can drop a line.
- No writes to GitHub. Thread replies are drafted to
  `<scratchpad>/replies/pr-<N>.md`, in the reply-file format the
  `review-change` skill's Answering gives.
- The report below.

When several parts need the same exploration, start one sub-agent that only
reads to do it first and write what it found to a notes file in your
scratchpad, and name that file in the rules.

## Each task

The task names the rules file; the plan and the part's ID; the repository,
branch, base and worktree; the part's goal and what is not its job; the
decisions already made, as final; and, where you know it, the commit each
correction folds into. It says what the part must do, not how, as
acceptance criteria each checkable on its own. A sub-agent that finds a fact contradicting a
decision, or a base it was given, stops that item and reports it instead of
following or reopening it. Work it finds outside the part's job it records
with `agent-plan followup PLAN --part P1 --title '...' --found-in <item>`
and leaves undone.

Each sub-agent reports, as its final message and within the length the task
gives, with no narrative:

- branch, worktree, and `git log --format='%h %G? %s' <base>..<branch>`
- what it changed, by plan item ID
- the exact commands it ran to verify, and their results
- for each fact it states, where it was observed: a command, a
  `file:line` or a URL
- what it could not verify, and what needs a decision

## Phases

Work that needs Wesley's decisions, such as review threads or a feedback list,
runs in phases.

1. **Investigate.** Nothing is committed. Each sub-agent checks every item
   against the code and tries to reproduce it, and gives a verdict with its
   evidence: real, not real, partly real, or cannot verify here. It proposes a
   fix and the commit the fix folds into, and may try the fix in its worktree,
   leaving it uncommitted.
2. **Decide.** Merge the reports into one list for Wesley. Give each decision
   its options and your recommendation.
3. **Fix.** One sub-agent per part, or per coupled stack, with Wesley's
   decisions written into its task. It ports any experiment from its
   investigation worktree, committing none of the scratch tests as they are.
4. **Publish**, once Wesley's signing pass has pushed. Re-read each thread,
   drop one already resolved or answered from its reply file, re-check each
   drafted reply against the pushed code, then post each file with
   `pr-loop reply --from`. Update each description to the change as it
   stands.

## Design review

Run the `review-change` skill's design review at two points, over every
part at once:

- before splitting: settle the structure, find the files parts share, and
  decide which branches stack;
- before Coalesce, once a batch of sub-agents has finished: cohesion,
  duplication, contradictions, the same file edited on parallel branches,
  and tooling that has drifted apart.

Its decisions go to Wesley as one list, merged with the Decide phase's when
there is one.

A finding by one sub-agent that bears on another part goes to that part's
sub-agent, in its current task or its next one.

## Model and effort

Start every sub-agent with the model and effort this section gives; it is
what permits setting them. In Claude Code they are the Agent tool's `model`
and `effort`. Codex's `spawn_agent` takes a `model`; ECA takes the model
from the agent's definition.

- **Haiku 5.5, low effort:** read-only lookups, inventories, running a given
  command and reporting it.
- **Sonnet 5.5, medium effort:** well-specified changes with tests and
  little judgement, such as a flag from a precise brief, wording edits, or a
  restack with few conflicts.
- **Opus 5.5, the default:** design reviews, adversarial reviews,
  security-sensitive code (the guard, the relay, `git sign-pass`,
  `pr-loop`), restacks with many conflicts, and anything ambiguous.
- Reviewers are never given a cheaper model: the reviewer is Codex by
  default, as the `review-change` skill's Harness choice says.

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
- **ECA:** `spawn_agent`. Each spawn starts a fresh chat, so a follow-up task
  carries everything the agent needs.
- **No sub-agents:** do each part yourself, one at a time, in its worktree.

Never edit a sub-agent's worktree while it runs. To learn where something
belongs in its part, ask it.

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

Once a report passes, log `done` for its part's ID, which marks its
heading `DONE` and readies the parts that wait for it. Relay only what you confirmed, and say what you did not.
Questions it raises go in the plan, as the `plan-work` skill's "Questions for Wesley"
says, not in your message.

## Review each branch

Before Coalesce, run the `review-change` skill's fresh review loop on each
branch by itself, never the whole stack at once: the range `<base>..<branch>`,
the part's goal and what is not its job as the goal statement, at most three
rounds. Hand each finding to the branch's sub-agent to settle as Triage
says, and check its report as any other.

## Coalesce

The branches go to Wesley as one piece of work. Before handing back, make
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
- Beside it, one `git sign-pass` command per repository, which Wesley types
  himself, naming the top of every stack, each child of a fork included.
  Flags come before the branch names: `--set-upstream` when any branch is
  new, otherwise `--push`.

  ```sh
  git sign-pass --set-upstream <top of stack 1> <top of stack 2> ...
  ```

  It prints the whole plan before the first touch; that plan is his audit.
  Never a script that wraps it: he must be able to read what he runs.
- The worktrees stay until the pass is done. Each must be clean, since the
  pass moves the branches checked out in them.
- Everything that needs Wesley's decision, and everything unverified.

## After the push

If you are still running once Wesley has pushed:

- Open a draft pull request for each new branch, based on its parent branch
  in a stack, then make each stack one GitHub stack, which GitHub shows on
  every pull request in it and merges from the bottom:
  `pr-loop link <bottom> ... <top>`, by number or branch. It refuses bases
  that do not chain, and leaves a stack already linked as it is.
  `pr-loop stack <pr>` shows GitHub's stack under `github`.
- Run the **Publish** phase: the drafted replies, and every description
  brought up to the change as it stands.
- Watch every pull request's CI as the `signing-pass` skill's "After a push"
  says; a fix is coalesced and handed back the same way.
- When nothing is left to fix, `git worktree remove` each worktree.
