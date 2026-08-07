---
name: restack-branches
description: Amend or rewrite a branch that has dependent branches stacked on it, then rebase and force-push the children. Use when a stacked pull request's base branch changes, when review feedback requires amending a parent commit, or when the whole stack must move onto an updated upstream.
---

# Restacking dependent branches

Rewriting a branch orphans every branch stacked on it. The children still point at
the *old* commits, so they must be replayed onto the new tip. `git rebase --onto`
needs the **old** parent tip as the exclusive lower bound, which is why it has to
be recorded before the parent is rewritten — afterwards it is only in the reflog.

## Procedure

**1. Record the shape before touching anything.**

```bash
git rev-parse --abbrev-ref HEAD                  # am I where I think I am?
OLD=$(git rev-parse <parent-branch>)             # keep this; --onto needs it
git log --oneline --graph <upstream>..<child>    # one per child
```

**2. Check out the parent and make the change there.** Not the child, not wherever
the last loop left you. Edits landing on the wrong branch of a stack are painful
to unpick.

**3. Amend or commit, and confirm it actually happened.**

```bash
BEFORE=$(git rev-parse HEAD)
git commit --amend --no-edit
AFTER=$(git rev-parse HEAD)
[ "$BEFORE" != "$AFTER" ] || echo "NOTHING WAS COMMITTED"
```

An unchanged SHA means the commit failed — commonly an unsigned hardware token.
Re-run the same command; do not proceed, and do not run anything destructive.

**4. Push the parent, then replay each child.**

```bash
git push --force-with-lease origin <parent-branch>
git rebase --onto <parent-branch> "$OLD" <child-branch>   # once per child
```

Children are siblings of each other, not a chain: each is replayed onto the
*parent*, each using the same `$OLD`.

**5. Validate every branch independently**, then push them together. A rebase that
applies cleanly can still produce a broken tree — a child may re-add content the
parent just deleted, or reference a symbol the parent renamed.

```bash
for b in <child-branches>; do
  git checkout -q "$b"
  <build/test/lint commands>
  grep -rn '^<<<<<<<' . && echo "unresolved markers in $b"
done
git push --force-with-lease origin <child-branches>
```

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
KEEP=$(git rev-parse HEAD)          # the commit to end up on top
git branch -f <backup-ref> HEAD     # reachable regardless of what follows
git reset --hard <upstream>
# ...make and commit the change that should come first...
git cherry-pick "$KEEP"
```

Verify the replayed commit is intact before deleting the backup ref:

```bash
diff <(git show "$KEEP" --stat --format='') <(git show HEAD --stat --format='')
```

## Afterwards

Stacked pull requests keep their bases across a force-push, but if the upstream
branch was deleted (merged), each base needs retargeting — the parent to the
default branch, children to the parent. That is a forge operation, so confirm
before doing it.
