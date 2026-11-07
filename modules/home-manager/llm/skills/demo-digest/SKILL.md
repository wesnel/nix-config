---
name: demo-digest
description: Write the week's demo digest on the notes host, running each change's demo steps and recording what they showed, then move approved brag items into the brag file.
disable-model-invocation: true
---

# Demo digest

The demo digest is what the operator shows in the weekly demo meeting:
each change merged or opened that week, with steps a viewer can follow
and the result each step gave when you ran it. Every result in it is
observed: output you saw, from the step exactly as the digest shows it.
Running the steps is the point: a step that fails is a bug found before
the meeting, and the digest shows it failing.

The weekly timer on the notes host (`plans.notesHost` in
`~/.config/agent-tools/config.json`) starts you headless, with ZONE, the
operator's time zone, as your prompt's argument (as
`America/Los_Angeles`). Nobody answers questions during the run: what you
would ask goes into the digest or the run's plan.

## 1. Start the run

1. Read today's date in ZONE, here `America/Los_Angeles`:

   ```sh
   TZ=America/Los_Angeles date '+%F %A'
   ```

   FRIDAY is today when today is a Friday, else the latest Friday before
   today. SINCE is seven days before FRIDAY. The week is SINCE through
   FRIDAY, both days included, in ZONE.
2. Make the run's plan, which records the host, the session and the
   directory you run in, and log its start against the ID it prints:

   ```sh
   agent-plan new --title 'Demo digest of 2026-11-06' --slug demo-digest
   ```

Done when FRIDAY, SINCE and the run's plan are known.

## 2. Find the week's changes

PLANS is `plans.dir` in `~/.config/agent-tools/config.json`, every
host's plans.

1. List the repositories whose pull requests any plan names, from the
   `:PULL_REQUESTS:` lines of PLANS (`grep -h '^:PULL_REQUESTS:' PLANS/*.org`).
2. For each repository, read its pull requests updated since SINCE; their
   times are UTC, so read each in ZONE:

   ```sh
   gh-read pr list -R wesnel/nix-config --author @me --state all --search 'updated:>=2026-10-30' --limit 100 --json number,title,url,state,createdAt,mergedAt
   ```

   A read that returns as many pull requests as its `--limit` may have
   left some out: read that repository again with the limit doubled,
   until a read returns fewer than its limit. If one still returns its
   limit at 800, name that repository as cut short, with the limit, in
   the digest's summary and in the hand-back.

3. A pull request is a change of the week when it was opened or merged
   during the week and is open or merged now.
4. Its plan is the file in PLANS whose `:PULL_REQUESTS:` names its URL. A
   part's file (`NAME.HOST.P1.org`) has no `:ID:`; its plan is the file
   its `:PARENT:` names. PLAN is `[[id:ID][TITLE]]`, from the plan file's
   `:ID:` and `#+title:`.

Done when every repository's last read returned fewer than its limit or
is named as cut short, and every pull request the reads returned is a
change of the week with its PLAN, or set aside with why (no plan names
it; closed unmerged; outside the week). If GitHub cannot be read, log `blocked` in the run's
plan with what `gh-read` printed, and stop: a digest without the week's
changes misleads.

## 3. Choose each change's steps

Read the change's Goal (`agent-plan goal PLAN`), its plan's
`** Verification`, and the pull request's description and branch:

```sh
gh-read pr view 244 -R wesnel/nix-config --json body,headRefName,mergeCommit,headRefOid
```

Read the diff where those leave the behaviour unclear. Choose up to five
steps that show a viewer the behaviour the change adds or fixes, each
giving output that tells the change apart from the code before it.

- Each step is a command a viewer pastes into a fresh shell at the top of
  a checkout of the change. A step hands state to a later step only
  through files under `.demo/` in that checkout.
- The first step builds or prepares what the rest run, into `.demo/`.
- A step that reads or writes configuration or state sets
  `XDG_CONFIG_HOME`, `XDG_STATE_HOME` and `XDG_DATA_HOME` under `.demo/`
  on its own line (and `HOME`, where the program reads files under `~`
  itself, as Claude Code and Codex do), so a viewer's run leaves their
  own state alone too.
- One sentence before each step says what it shows; it becomes the
  step's text in the digest.

Done when every change has its steps, each with the sentence saying what
it shows.

## 4. Run each step

1. Make the change's throwaway checkout in a new directory from
   `mktemp -d`: clone the operator's checkout under `~/git/github.com/`
   where there is one, else the repository from GitHub; then check out
   the merge commit of a merged change, or fetch `pull/N/head` from
   GitHub for an open one.
2. Run each step from the checkout's top directory, in a fresh shell,
   exactly as the digest will show it, and keep its output (standard
   output and error together) and exit status.

What a step changes stays inside the checkout and `$TMPDIR`. The operator's
plans, notes repository, `~/.claude`, `~/.codex`, `~/.config`, systemd
units and GitHub are left as they are: no switch, push, `pr-loop` write,
or `agent-plan` write against the real plans. A refusal by the sandbox or
the guard is an observed result to record as step 5 says.

Done when every step has run once, or is recorded as not run.

## 5. Record what was observed

- **Result:** the output as it appeared, cut to the lines that show the
  behaviour, each cut marked `[... N lines cut]`. With no output, write
  `(no output, exit 0)`; with a non-zero exit, end with `exit N`.
- **Failing step:** its output is not what its sentence says it shows, or
  it exits non-zero unexpectedly. Its sentence starts `FAILS:` and says
  what was expected, and the result holds what happened. Record it with
  `agent-plan followup` in the run's plan, naming the pull request and
  the step. The step stays as written and run: a fix is the change's own
  work, after the meeting.
- **Step the devbox cannot run:** it needs macOS (launchd, the Keychain,
  Parallels), the hardware key, a Mac-hosted service, or a write the
  sandbox refuses. First look for a step the devbox can run that shows
  the same behaviour. If there is none, keep the step; its sentence
  starts `NOT RUN:` with why, and its result is `not run on HOST: REASON`.
  Its result is never an expected output.

Done when every step has a result that is output you saw, or one of the
two markings above.

## 6. Write the digest

The digest is Org that `agent-plan digest` checks and the Agent plans
page parses:

```org
Written on devbox for SINCE to FRIDAY (ZONE), in the run's plan NAME.
Set aside: wesnel/notes#3, which no plan names.

* Keep each change's visuals in notes
:PROPERTIES:
:PLAN: [[id:867f8a68-d069-4a03-9d20-a2c14ae0c2d5][Builds on line, and visuals]]
:PR: https://github.com/wesnel/nix-config/pull/244
:END:
Merged 2026-11-05. 2 of 3 steps pass; one fails.

Build agent-plan from the merged commit.
#+begin_src sh
go build -o .demo/agent-plan ./overlays/agent-tools/cmd/agent-plan
#+end_src
#+begin_example
(no output, exit 0)
#+end_example

#+caption: How a visual reaches the digest
#+begin_src mermaid
flowchart LR
    N1["Review record"]
    N2["Plan Visuals"]
    N1 -->|"agent-plan visual"| N2
#+end_src

[[https://github.com/user-attachments/assets/0f1e2d3c][Recording: agent-plan visual]]
```

- Before the first change: one paragraph saying where and for which week
  the digest was written, and naming each pull request set aside, with
  why.
- One level-1 heading per change, its title the pull request's, with the
  property drawer right below holding `:PLAN:` and `:PR:`, then one
  paragraph: merged or opened, on which day, and how many steps pass.
- Each step: its sentence as a paragraph, its command in a `sh` source
  block, its result in the `example` block right after. A line of a
  block that starts with `*` or `#+` takes a leading comma (`,* `), as
  Org escapes it; unescaped, it reads as a new change.
- Visuals: each entry of the plan's `** Visuals` whose `:BRANCH:` is the
  pull request's `headRefName`. A diagram's `mermaid` block is copied as
  it is, after `#+caption:` and the entry's title. An entry's `:URL:` is a
  link on a line of its own, `[[URL][TITLE]]`. A tape's source stays in
  the plan; its recording's URL is the link.
- The whole digest stays under 64 KiB.

Write the text to a file in your working directory with the file tools,
then write the digest from it:

```sh
agent-plan digest --date 2026-11-06 --text-file digest.org
```

If it refuses, change what the refusal names and run it again.

Done when `agent-plan digest` prints `"changed": true`, or `false` for a
digest already holding the same text.

## 7. Move approved brag items

Load the `brag` skill and move the approved brag items whose change has
merged into the brag file, as its "Moving approved items into the brag
file" says. Read whether a pull request merged, and on which day in
ZONE, with the same `gh-read pr view` as step 3, asking for
`state,mergedAt`.

Done when that section's own criterion holds.

## 8. Hand back

Log `done` against the run's plan ID, with the evidence the digest's
file and its counts: changes, steps passing, failing and not run, and
brag entries written. End with the same line as your reply.
