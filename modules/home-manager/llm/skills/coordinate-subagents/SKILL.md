---
name: coordinate-subagents
description: Split work with several parts across sub-agents, each doing one part on its own branch in its own git worktree, while you coordinate, decide what spans parts, check every report and hand back to Wesley. Use when a task has several parts that could proceed in parallel, such as several pull requests or a stack of them, open review threads across PRs, a list of feedback items, or several independent changes.
---

# Coordinating sub-agents

You coordinate. One sub-agent per part does that part's work on its own
branch, in its own worktree. You own the split, the order and base of every
stacked branch, the decisions that span parts, the check of every report and
the hand-back. Nothing reaches GitHub until Wesley has signed and pushed.

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

- One part is one branch, one worktree and one sub-agent.
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

## The rules file

Write the working agreements once, to a file in your scratchpad, and start
every task with "First read <file> and follow it exactly." Do not count on a
sub-agent having loaded AGENTS.md or the skills you have. Put in it:

- Work only in the worktree the task names, never the main checkout or another
  worktree. Run `git branch --show-current` immediately before every edit and
  every commit. Never `git stash`: every worktree shares one stash.
- The `signing-pass` rules: `-c commit.gpgsign=false` on everything that
  writes commits, a check that `git rev-parse HEAD` moved, and never a push,
  tag or signature.
- The commit rules from AGENTS.md: atomic history, a correction as
  `--fixup=<commit>` of the commit that owns the code, the subject rules, no
  trailers, explicit paths only.
- File-editing tools only, the fish shell, no `sudo`, `trash` instead of
  `rm`, and no wrapper shell scripts.
- The documentation rules from AGENTS.md.
- How to build, test and lint in this repository, which commands need the
  sandbox off, and what a fresh worktree lacks (generated files, for
  example).
- Before reporting done: a trial autosquash on a throwaway branch, deleted
  afterwards, with the tests and linters run on the squashed result, since a
  squash can drop a line. For each bug fix, the new test fails without the
  fix.
- No writes to GitHub. A thread reply is drafted to
  `<scratchpad>/replies/pr-<N>.md`, one section per thread: its ID, the reply,
  and `resolve: yes|no`. A reply names commits by subject, never by SHA,
  because the signing pass changes every SHA.
- The report below.

## Each task

The task names the rules file; the repository, branch, base and worktree; the
part's goal and what is not its job; the decisions already made, as final;
and, where you know it, the commit each correction folds into. A sub-agent
that finds a fact contradicting a decision, or a base it was given, stops
that item and reports it instead of following or reopening it.

Each sub-agent reports, as its final message:

- branch, worktree, and `git log --format='%h %G? %s' <base>..<branch>`
- what it changed, item by item
- the exact commands it ran to verify, and their results
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
   skip one already resolved or answered, re-check each drafted reply against
   the pushed code, then reply in the thread and resolve it as AGENTS.md says.
   Update each description to the change as it stands.

A finding by one sub-agent that bears on another part goes to that part's
sub-agent, in its current task or its next one.

## Running them

Start independent sub-agents together, in the background.

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
  the report: every commit `N`, no trailers, subjects in shape, on the base
  you named. Read the commits that matter with `git show`.
- Its verification commands pass when you run them.

Relay only what you confirmed, and say what you did not.

## Hand back

- For every branch, bottom of each stack first,
  `git log --format='%h %G? %s' <base>..<branch>`.
- The signing passes as the `signing-pass` skill gives them, one per fork
  tip, as commands Wesley types himself. Never a script that wraps them: he
  must be able to read what he runs.
- The worktrees stay until the passes are done; then `git worktree remove`
  each.
- Everything that needs Wesley's decision, and everything unverified.
