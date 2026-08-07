# Working agreements

## Signed commits and hardware tokens

Some repositories sign commits with a hardware token that needs a physical touch,
and the touch is easy to miss. Signing can fail **loudly** (`fatal: failed to
write commit object`, `gpg failed to sign the data`, `agent refused operation`) or
**silently** — the command prints hook output and appears to succeed while `HEAD`
never moves.

- Capture the SHA before and after every `commit`, `--amend`, `rebase --continue`
  and `cherry-pick`, and assert it changed. Do not trust the exit status alone.
- A failure is usually just a missed touch, not a broken repository. Re-run the
  identical command so it can be approved. Do not diagnose it, and never work
  around it with `--no-gpg-sign`, `--no-verify`, or by disabling signing.
- Until the commit is confirmed, treat the work as uncommitted: no `reset --hard`,
  no `checkout -f`, no rebase that assumes it landed. Losing a working tree to an
  unnoticed signing failure is the failure mode to design against.

## Know which branch you are on

Confirm the checked-out branch immediately before starting an edit, not once at
the beginning of a task. Rebases, cherry-picks and per-branch loops all leave the
repository somewhere other than where the last edit happened, and edits landing on
the wrong branch of a stack are expensive to unpick.

## Editing files

Use the file-editing tools, not `sed`, `perl`, `python`, or shell redirection.
Editing several files, or making the same change many times, is not an exception.

## Documentation and comments

Say what someone needs in order not to break things. Do not record how the current
state came about.

- No decision narratives, no version archaeology, no restating what the adjacent
  line plainly does.
- **Do not document an absence.** Explaining why a section, option or abstraction
  is *not* there is noise; the reader is not looking for it.
- **Temporal state does not belong in the repository.** "Not yet wired up",
  "today", "before the first release", "once X lands" all rot the moment someone
  acts on them. That content belongs in the pull request description or the
  ticket, which expire naturally.
- **Put a constraint next to the thing it constrains** — the config key, the
  function, the workflow step. Collected lists of gotchas drift out of sync and
  duplicate what is already written at the point of use. A central document should
  hold only what has no such home.

## Verify before asserting

For how a tool, platform or API actually behaves, test it rather than recalling
it — especially where the plausible answer and the true one differ. State what was
observed. When a claim cannot be checked in the current environment, say so
explicitly instead of implying it was verified.

The same applies to review feedback, human or automated: confirm the problem is
real, and confirm the fix works, before reporting either. A reviewer being wrong is
worth demonstrating politely; a reviewer being right about something subtle is
worth acting on even when the exploit does not reproduce.

## Prefer git to gh

When a question can be answered with `git` — does a ref exist, what changed, which
commit is an ancestor — use `git`. Reach for `gh` only for things that genuinely
live in the forge: pull requests, reviews, workflow runs, releases.

## Stay inside the scope you were given

Do not edit files owned by someone else's in-flight work to make an unrelated
improvement, even a correct one. Raise it instead. This applies to relocating
content into another team's document as much as to changing their code.
