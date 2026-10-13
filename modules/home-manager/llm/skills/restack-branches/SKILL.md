---
name: restack-branches
description: Amend or rewrite a branch that has dependent branches stacked on it, then rebase the children onto it. Use when a stacked pull request's base branch changes, when review feedback requires amending a parent commit, or when the whole stack must move onto an updated upstream.
---

# Restacking dependent branches

Rewriting a branch orphans every branch stacked on it. The children still point at
the *old* commits, so they must be replayed onto the new tip. `git rebase --onto`
needs the **old** parent tip as the exclusive lower bound, which is why it has to
be recorded before the parent is rewritten — afterwards it is only in the reflog.

## Procedure

**0. Work in a dedicated worktree.** Any branch operation, including switching
branches only to read a file, happens in a worktree of its own, never in the main
clone. The main clone often holds Wesley's untracked work in progress, and a
checkout or reset there can delete it, even a plain `git switch` once history has
been rewritten underneath it.

```bash
git worktree add <scratch>/restack --detach <parent-branch>
```

Stage explicit paths there too, never `git add -A`.

**1. Map the stack and keep a backup of every branch.** Stacks fork, so the
branches form a tree. For each branch, the deepest other branch that is its
ancestor (`git merge-base --is-ancestor`) is its parent; the branches no other
branch builds on are the fork tips.

```bash
git rev-parse --abbrev-ref HEAD                  # am I where I think I am?
OLD=$(git rev-parse <parent-branch>)             # keep this; --onto needs it
for b in <every branch in the stack>; do
  git update-ref "refs/backup/$b" "$b"           # not a branch: --update-refs moves branches
done
```

**2. Check out the parent and make the change there.** Not the child, not wherever
the last loop left you. Edits landing on the wrong branch of a stack are painful
to unpick.

**3. Fold the change into the commit that owns it**, unsigned, and confirm it
landed. Commit it as a fixup of that commit, which the signing pass squashes in
(the `signing-pass` skill covers this):

```bash
BEFORE=$(git rev-parse HEAD)
git -c commit.gpgsign=false commit --fixup=<owning-commit>
AFTER=$(git rev-parse HEAD)
[ "$BEFORE" != "$AFTER" ] || echo "NOTHING WAS COMMITTED"
```

An unchanged SHA means the commit failed. Do not proceed, and do not run
anything destructive.

**4. Replay each fork tip once.** `--update-refs` moves every branch between the
parent and the tip along with it, so a chain of many branches takes one rebase:

```bash
git -c commit.gpgsign=false rebase --update-refs --onto <parent-branch> "$OLD" <fork-tip>
```

Forks that start at the same parent each get their own rebase, each with the
same `$OLD`.

**5. Validate every branch independently.** First check that each branch differs
from its backup only in the files you meant to change, which catches a bad
conflict resolution anywhere in the replay:

```bash
for b in <every branch in the stack>; do
  echo "== $b"; git diff --name-only "refs/backup/$b" "$b"
done
```

A rebase that applies cleanly can still produce a broken tree. A child may re-add
content the parent just deleted, or reference a symbol the parent renamed, so
build and test each branch:

```bash
for b in <every branch in the stack>; do
  git checkout -q "$b"
  <build/test/lint commands>
  grep -rn '^<<<<<<<' . && echo "unresolved markers in $b"
done
```

**6. Hand over for the signing pass**, which also pushes. Run on a fork tip,
`git sign-pass --push` squashes the fixups, re-signs the tip and the branches
below it, and pushes every branch that moved. Each fork tip needs its own pass.
Once everything is pushed, the backups can go:
`git for-each-ref --format='%(refname)' refs/backup/ | xargs -n1 git update-ref -d`.

## Resolving conflicts in a restack

The parent's new content is `HEAD`/`--ours`; the child's commit being replayed is
`--theirs`. Two patterns recur:

- **The child re-adds something the parent removed.** If the parent deleted or
  relocated content deliberately, drop the child's copy — do not resurrect it
  because the conflict presented it.
- **Both added adjacent content.** Usually both belong; merge them rather than
  choosing, then re-read the result in full, since Git will happily produce
  something syntactically valid and semantically wrong.

## Reordering commits without interactive rebase

Interactive rebase is often unavailable. To insert a commit *before* an existing
one on the same branch:

```bash
KEEP=$(git rev-parse HEAD)                        # the commit to end up on top
git update-ref refs/backup/<branch> HEAD          # reachable regardless of what follows
git reset --hard <upstream>                       # in the dedicated worktree only
# ...make and commit (unsigned) the change that should come first...
git -c commit.gpgsign=false cherry-pick "$KEEP"
```

Verify the replayed commit is intact before deleting the backup ref:

```bash
diff <(git show "$KEEP" --stat --format='') <(git show HEAD --stat --format='')
```

## Afterwards

Stacked pull requests keep their bases across the signing pass's push, but if the upstream
branch was deleted (merged), each base needs retargeting — the parent to the
default branch, children to the parent. That is a forge operation, so confirm
before doing it.
