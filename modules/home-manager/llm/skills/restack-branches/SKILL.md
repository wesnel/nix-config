---
name: restack-branches
description: Amend or rewrite a branch that has dependent branches stacked on it, then rebase the children onto it. Use when a stacked pull request's base branch changes, when review feedback requires amending a parent commit, or when the whole stack must move onto an updated upstream.
---

# Restacking dependent branches

Rewriting a branch orphans every branch stacked on it. The children still point at
the *old* commits, so they must be replayed onto the new tip. `git rebase --onto`
needs each child's **fork point**, where it left the parent, as the exclusive
lower bound, which is why it has to be recorded before the parent is rewritten:
afterwards `git merge-base` no longer finds it.

## Procedure

**0. Work in a dedicated worktree.** Any branch operation, including switching
branches only to read a file, happens in a worktree of its own, never in the main
clone. The main clone often holds Wesley's untracked work in progress, and a
checkout or reset there can delete it, even a plain `git switch` once history has
been rewritten underneath it.

```bash
git worktree add <scratch>/restack --detach <parent-branch>
pr-loop setup <scratch>/restack                  # where pr-loop is installed
```

`pr-loop setup` runs the setup command Wesley configured for the repository,
such as generating files it does not commit, and does nothing where none is
configured. Run it again in the worktree after each checkout or rebase there.

Stage explicit paths there too, never `git add -A`.

**1. Map the stack and keep a backup of every branch.** Stacks fork, so the
branches form a tree. For each branch, the deepest other branch that is its
ancestor (`git merge-base --is-ancestor`) is its parent; the branches no other
branch builds on are the fork tips. Record the fork point of every child of the
branch you are about to change, one per fork that starts there. If the
branches will be replayed one at a time (step 4), record every branch's fork
point on its own parent instead:

```bash
git rev-parse --abbrev-ref HEAD                  # am I where I think I am?
for c in <each child of the parent-branch>; do   # --onto needs these
  git update-ref "refs/fork-point/$c" "$(git merge-base "$c" <parent-branch>)"
done
for b in <every branch in the stack>; do
  git update-ref "refs/backup/$b" "$b"           # not a branch: --update-refs moves branches
done
```

Record them again before every later change to the parent.

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
git -c commit.gpgsign=false rebase --update-refs --onto <parent-branch> refs/fork-point/<child> <fork-tip>
```

`<child>` is the branch on that fork directly above the parent. Forks that
start at the same parent each get their own rebase, each with its own child's
fork point. Always take it from the record, never from a parent tip remembered
from earlier: a tip from before an extra fixup on the parent leaves that fixup
in the range, and the rebase can replay it onto the new parent as a duplicate.

`--update-refs` silently skips a branch checked out in another worktree, such
as the ones `pr-loop worktrees` makes during a review loop. When the stack's
branches are checked out elsewhere, replay them one at a time, bottom-up, each
in the worktree that has it, onto its own parent from its own recorded fork
point. Either way, check afterwards that every branch in the stack moved.

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

**6. Hand over for the signing pass**, which also pushes, as one command that
names every fork tip. `git sign-pass --push <fork-tip>...` squashes the
fixups, re-signs each tip and the branches below it, and pushes every branch
that moved; a branch never pushed before needs `--set-upstream` in place of
`--push`. Where forks share a parent, the pass signs the parent once
and replays the other forks onto the signed copy from their fork points itself.
Once everything is pushed, the backups can go:
`git for-each-ref --format='%(refname)' refs/backup/ refs/fork-point/ | xargs -n1 git update-ref -d`.

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
