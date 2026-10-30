---
name: signing-pass
description: How to commit in the operator's repositories, whose commits are signed with a hardware key that needs a physical touch. The commit rules, committing unsigned, and handing over one signing pass at the end. Use whenever creating, amending or rewriting commits, or when a commit fails with a gpg or signing error.
---

# Unsigned work, one signing pass

Git is configured to sign every commit, and signing waits for a touch on
the operator's hardware key. In a session nobody is watching, that wait blocks
the session until someone notices. So commit unsigned while you work, and leave
the signing to one pass at the end, which the operator runs.

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
- Stage explicit paths. Never sweep a repository's main checkout with
  `git add -A`, `--all` or `.`, with or without a directory: it holds
  the operator's untracked work in progress.

## While working

- Every command that writes commits takes `-c commit.gpgsign=false`:
  `commit`, `commit --amend`, `rebase`, `cherry-pick`, `revert`, `merge` and
  `am`. For example:

  ```sh
  git -c commit.gpgsign=false commit -m "Say what changed"
  git -c commit.gpgsign=false rebase --onto main old-base feature
  ```

  A rebase re-signs every commit it replays unless told not to, so it needs
  the flag as much as a commit does.
- A repository you create, a test's fixture or a throwaway clone, inherits
  the same config: its commits take the flag too, in test code as well, or
  its own config sets `commit.gpgsign` to false.
- Never `git push`. A push to GitHub authenticates with the same key over SSH
  and waits for a touch too. Leave it to the signing pass; a branch GitHub
  does not have yet gets its draft pull request as the `pull-requests`
  skill's Opening says, with no touch.
- Never create tags; they are signed as well.
- If a command fails with `gpg failed to sign the data`, `failed to write
  commit object` or `agent refused operation`, or hangs, the flag was
  missing: run the same command again with it. A signing failure can also be
  silent, so compare `git rev-parse HEAD` before and after each commit, and
  treat the work as uncommitted until `HEAD` has moved. Never work around a
  failure with `--no-verify`.
- In a repository the operator signs, these are the only unsigned commits there
  should be. Don't sign anything yourself.

## Handing over

When the work is done, show the operator what the pass will sign, for each
branch you committed to:

```sh
git log --format='%h %G? %s' origin/main..HEAD
```

`N` marks an unsigned commit. The pass starts from the same place: where the
branch left the remote's default branch (`origin/HEAD`, then `origin/main` or
`origin/master`, then local `main` or `master`), so a stale local `main` does
not matter. For a branch that starts from somewhere else, put that ref in the
range, hand over `--base REF`, and sign that branch in a pass of its own.

Then ask `pr-loop next` for each branch's next step, and take that step:

```sh
pr-loop next <branch> ...
```

It only reads, and prints one step per branch:

- `open`: the branch's draft pull request, yours to open as the
  `pull-requests` skill's Opening says; run `pr-loop next` again afterwards.
  A branch GitHub lacks can open unsigned; one already on GitHub needs
  verified signatures by the protected operator key.
- `sign` or `push`: the operator's command, a `git sign-pass` with the
  right push flag, and the `agent-plan sign-id` line
  that mints its `--notify` ID, as "Hearing how it ended" says.
- `blocked`: what to fix first, such as an upstream of another name or a
  remote-tracking ref behind GitHub. If GitHub has history the local branch
  lacks, fetch and reconcile it by hand; fix it and ask again.
- `nothing`: GitHub has the signed tip.

The forms of the pass:

```sh
git sign-pass                  # sign; nothing is pushed
git sign-pass --push           # sign, then push to its own name upstream every branch that moved, or that is ahead of its remote, refusing any commit the operator's key did not sign
git sign-pass --set-upstream   # the same, and publish those that have no upstream yet
```

The pass pushes one branch per `git push`, bottom up, since GitHub runs a pull
request's CI twice when one push moves it and its base; any push you suggest
for a stack does the same, one branch per command, bottom up. `--set-upstream`
refuses a branch the remote already has, and both refuse, before any touch, a
branch whose upstream is a branch of another name and any push to the
remote's default branch.

When the fixups answer a review bot's threads outside a bots-mode loop,
the push line you hand over also asks the bots to review again; the
operator runs it once they have posted the replies, as the
`review-change` skill's Answering says.

For more than one branch to sign, hand over one command that names the top of
every stack, run from any worktree of the repository:

```sh
git sign-pass --push <top of stack 1> <top of stack 2> <each child of a fork> ...
```

It signs each stack from wherever its branch is checked out, replays a fork's
other children onto the signed parent, and prints the whole plan, with the key
touches, before the first touch, and that plan is the operator's audit. The
operator types the pass themselves, so do not write a script around it: they
must be able to read what they run.

It re-signs every commit from the oldest unsigned one to the tip, and moves any
branch stacked inside that range along with it. A stacked branch checked out in
another worktree moves too, and that worktree's files with it, so the worktree
must have no uncommitted or untracked files (ignored ones may stay); otherwise
the pass refuses before any touch. A stacked branch lands on the signed copy
of the last of its commits that squashing keeps: one whose tip is a fixup
lands on the nearest commit below the tip that remains. `fixup!`, `amend!`
and `squash!` commits are squashed into the commits they name as part of the
same pass; an `amend!` replaces its target's whole message with its own body,
and a `squash!` appends its body, without opening an editor. It tries the
squash unsigned first and refuses before any touch if it would conflict. If
nothing is squashed, it refuses when a commit's files, author or message
would change. If any of the three kinds is squashed, it refuses when the
remaining commits or the files at the tip would differ from that. Before the
first touch it names any branch lower in the stack that gains a fix from
commits it does not contain, and it reports each again once signed. It needs
one touch per commit in the range, folds included, since git signs each step
of a squash, plus one for each push URL. At a missed touch in a terminal, it
asks to retry that step. With no terminal (an agent's run, or a `!` run) it
retries once by itself, after a wait and under a timeout, because one missed
touch locks the key and the next request then waits on pinentry without end.
If it stops, it prints the exact commands that carry on and the touches they
need, which `--resume` only reports, never changing a rebase in progress, and
the unsigned branch is kept under `refs/sign-pass/backup/`. After a stop that
mentions pinentry, the operator unlocks the key before the resume.

Run in a session or with `!`, where standard output is not a terminal, it
prints brief output: one line of what it will sign, a line to touch the key,
a progress line every 25 signatures, one line per push and the JSON summary,
with git's own output only when a command fails (`--no-brief` brings it
back). The operator's `!` run in the session is fine: its output will not
flood the context.

Aim a fixup at the commit that introduced the code. If a later commit changed
the same lines, aim it there instead, or the squash will conflict. To change
an earlier commit's message, use `--fixup=reword:<commit>` (or `amend:` with
changes), whose message after the `amend!` line becomes that commit's whole
message.

During a review loop (the `review-loop` skill), the commits are also
fixups, which this pass squashes as well as signing them, once `pr-loop
sign-check` has passed. On a ready PR, `pr-loop push` takes only
`--fixup=<commit>`, never `amend!` or `squash!`, since the loop must not
change the PR's commit messages.

## Hearing how it ended

The operator runs the pass in their own terminal, possibly hours later, and
does not tell you when it ends. The pass reports back itself. Before handing
it over, mint an ID in the worktree you committed in, naming each branch it
signs:

```sh
agent-plan sign-id --branch <branch>
```

It logs the request in the session's plan and prints the ID; without a plan
it refuses. Add `--notify <ID>` to every `git sign-pass` command you hand
over, `--resume` included. When the pass ends, signed, refused or failed, it
logs the result in the plan, and the session's next prompt carries one line
for it, such as `sign-pass ID signed: 2 signed; pushed <branch>` or
`sign-pass ID refused: <reason>`. A refusal or failure is yours to fix and
hand over again. A line marked stale is for the record: take no follow-up.

- In Claude Code, also start a waiter with the Bash tool's
  `run_in_background`, as a command of its own, so the session wakes when the
  pass ends rather than at the operator's next prompt. It gives up after a
  day:

  ```sh
  agent-plan sign-result <ID> --wait
  ```

  The result arrives with its notification; `agent-plan sign-result <ID>`
  prints it again.
- In Codex, the result arrives with the operator's next prompt.
- Never poll for the result with model turns.

## After a push

After every push to a pull request that you make or are told about,
including `pr-loop open` and the operator's `git sign-pass --push`, watch
its CI if you are still running. Run this in the background, naming the
pull request's number or branch, as a command of its own with no `cd`
before it, with the Bash tool's `run_in_background` or your harness's
equivalent, and act when it exits:

```sh
pr-loop wait ci --timeout 3600 PR
```

It waits for the pushed commit to become the pull request's head, so it
never reports the previous head's checks, and prints a `result`: `passed`,
`failed`, `failedOnBase`, `noChecks` (exit 4) or `timedOut` (exit 3). Each
failed check names the `pr-loop logs` command for its log and says whether
it fails on the base too (`onBase`).

- A failure the pull request caused: fix it as a fixup, as above, and hand
  over again.
- A failure that is not its doing (failing on the base, a flaky test, an
  infrastructure fault): report it to the operator with the evidence from
  the log. Never rerun CI or push again in the hope that it passes.
- `noChecks`: no check reported on the head within `--grace` (default three
  minutes). Tell the operator that CI did not run; it is not a pass.
