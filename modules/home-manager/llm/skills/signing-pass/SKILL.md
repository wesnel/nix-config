---
name: signing-pass
description: How to commit in Wesley's repositories, whose commits are signed with a hardware key that needs a physical touch. Commit unsigned while working, never trigger a touch, and hand over for one signing pass at the end. Use whenever creating, amending or rewriting commits, or when a commit fails with a gpg or signing error.
---

# Unsigned work, one signing pass

Git is configured to sign every commit, and signing waits for a touch on
Wesley's hardware key. In a session nobody is watching, that wait blocks the
session until someone notices. So commit unsigned while you work, and leave the
signing to one pass at the end, which Wesley runs.

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
- Never push. A push to GitHub authenticates with the same key over SSH and
  waits for a touch too. Leave it to the signing pass.
- Never create tags; they are signed as well.
- If a command fails with `gpg failed to sign the data`, `failed to write
  commit object` or `agent refused operation`, or hangs, the flag was
  missing: run the same command again with it. A signing failure can also be
  silent, so compare `git rev-parse HEAD` before and after each commit, and
  treat the work as uncommitted until `HEAD` has moved. Never work around a
  failure with `--no-verify`.
- In a repository Wesley signs, these are the only unsigned commits there
  should be. Don't sign anything yourself.

## Handing over

When the work is done, show Wesley what the pass will sign, for each branch
you committed to:

```sh
git log --format='%h %G? %s' main..HEAD
```

`N` marks an unsigned commit. Then tell Wesley to run, in that branch, in a
terminal of their own:

```sh
git sign-pass                  # sign; nothing is pushed
git sign-pass --push           # sign, then push every branch that moved to its own name upstream
git sign-pass --set-upstream   # the same, and publish moved branches that have no upstream yet
```

Suggest `--set-upstream` when a branch has never been pushed: it publishes the
branch in the same push, and refuses one the remote already has. Both refuse,
before any touch, a branch whose upstream is a branch of another name, and any
push to the remote's default branch.

For more than one branch, hand over one command that names the top of every
stack, run from any worktree of the repository:

```sh
git sign-pass --push <top of stack 1> <top of stack 2> <each child of a fork> ...
```

It signs each stack from wherever its branch is checked out, replays a fork's
other children onto the signed parent, and prints the whole plan, with the key
touches, before the first touch. Do not write a script around it.

It re-signs every commit from the oldest unsigned one to the tip, and moves any
branch stacked inside that range along with it. A stacked branch checked out in
another worktree moves too, and that worktree's files with it, so the worktree
must have no uncommitted or untracked files (ignored ones may stay); otherwise
the pass refuses before any touch. `fixup!`, `amend!` and `squash!` commits are
squashed into the commits they name as part of the same pass; an `amend!`
replaces its target's whole message with its own body, and a `squash!` appends
its body, without opening an editor. It tries the squash
unsigned first and refuses before any touch if it would conflict. If nothing is
squashed, it refuses when a commit's files, author or message would change. If
any of the three kinds is squashed, it refuses when the remaining commits or the
files at the tip would differ from that, and it reports any branch lower in the stack that
gained a fix. It needs one touch per remaining commit, plus one for each push. If it
stops at a missed touch, it says how to resume, and the unsigned branch is kept
under `refs/sign-pass/backup/`. Pass `--base REF` when the branch does not start
from `main`.

So a correction to an earlier commit is a fixup, not a commit of its own:

```sh
git -c commit.gpgsign=false commit --fixup=<the commit it corrects>
```

Aim it at the commit that introduced the code. If a later commit changed the
same lines, aim it there instead, or the squash will conflict. To change an
earlier commit's message, use `--fixup=reword:<commit>` (or `amend:` with
changes), whose message after the `amend!` line becomes that commit's whole
message.

During a bot review loop (the `review-loop` skill), the commits are also
fixups, and that loop's signing pass squashes them as well as signing them.
That loop accepts only `--fixup=<commit>`, never `amend!` or `squash!`, since
it must not change the PR's commit messages.
