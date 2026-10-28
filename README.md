<!-- markdown-toc start - Don't edit this section. Run M-x markdown-toc-refresh-toc -->
**Table of Contents**

- [directory structure](#directory-structure)
- [adding new systems](#adding-new-systems)
    - [secrets](#secrets)
        - [add the machine's age key to sops config](#add-the-machines-age-key-to-sops-config)
        - [update keys with new host](#update-keys-with-new-host)
- [coding agents](#coding-agents)
    - [what nix sets up](#what-nix-sets-up)
    - [committing: unsigned work, one signing pass](#committing-unsigned-work-one-signing-pass)
    - [the review loop](#the-review-loop)
        - [starting a loop](#starting-a-loop)
        - [after the loop](#after-the-loop)
        - [what the wrapper adds](#what-the-wrapper-adds)
    - [MCP servers](#mcp-servers)
- [replacing the devbox](#replacing-the-devbox)
- [updating systems](#updating-systems)
    - [nixOS](#nixos)
    - [orbstack nixOS virtual machine](#orbstack-nixos-virtual-machine)
    - [nix-darwin](#nix-darwin)
    - [home-manager](#home-manager)
    - [devbox](#devbox)
- [troubleshooting](#troubleshooting)
    - [nixOS](#nixos-1)
        - [stale lockfiles in `.gnupg/public-keys.d/` cause gpg to hang](#stale-lockfiles-in-gnupgpublic-keysd-cause-gpg-to-hang)
        - [error with `command-not-found` helper function](#error-with-command-not-found-helper-function)
    - [macOS](#macos)
        - [`$PATH` gets mangled](#path-gets-mangled)

<!-- markdown-toc end -->
# directory structure

```
.
├── machines
│  ├── nixos
│  │  └── nixOS system configurations
│  └── darwin
│     └── darwin system configurations
├── modules
│  ├── nixos
│  │  └── opinionated nixOS configuration modules
│  ├── home-manager
│  │  └── opinionated home-manager configuration modules
│  └── darwin
│     └── opinionated darwin configuration modules
├── overlays
│  └── packages built here, including agent-tools (see coding agents)
├── flake.lock
├── flake.nix <-- main entrypoint
├── LICENSE.txt
└── README.md
```

# adding new systems

## secrets

You will need to configure your system with all necessary secrets via [sops-nix](https://github.com/Mic92/sops-nix).

### add the machine's age key to sops config

The machine's age key is generated during its first activation. Read the public
half of it on that machine:

``` bash
nix-shell -p age --run "age-keygen -y ~/.config/sops-nix/key.txt"
```

Add that to `.sops.yaml` by following the pattern set in that file for other
machines: an anchor under `keys`, and an entry in the `age` list of the creation
rule.

### update keys with new host

``` bash
nix-shell --run "sops updatekeys secrets/wgn.yaml"
```

# coding agents

Claude Code (`wgn.home.claude`) and Codex (`wgn.home.codex`) share one
setup.

## what nix sets up

Enabling any of those modules enables `wgn.home.llm`, which provides:

- **Instructions.** `modules/home-manager/llm/AGENTS.md` is linked to where
  each agent reads its user-level instructions.
- **Skills.** `wgn.home.llm.skills` are linked into each agent's skill
  directory.
- **`agent-tools`** on `PATH`: `git sign-pass`, `git red-check`,
  `git delegate`, `pr-loop` and `agent-plan`, built from
  `overlays/agent-tools`. `wgn.home.git` uses
  its `git-push-guard` as well.

Agents write the files below themselves, so Nix merges its keys into them on
every switch rather than owning them. Everything else in them is left alone,
and a change to one of these keys made from inside an agent is undone by the
next switch.

| File | Keys set on every switch |
| --- | --- |
| `~/.claude/settings.json` | `wgn.home.claude.settings`: attribution off, the ngrok and reviewer hooks, and the plan hook on session resume, compaction or `/clear` (a hook list here replaces the file's own) |
| `~/.claude.json` | `mcpServers` entries for the MCP proxy |
| `~/.codex/config.toml` | `[mcp_servers.*]` tables for the MCP proxy |

## committing: unsigned work, one signing pass

Commits and pushes are signed with a YubiKey that needs a touch, so agents
commit unsigned, fold corrections into the commit they correct with
`git commit --fixup`, and never `git push` or tag. The instructions and the
`signing-pass` skill tell them so. To open a pull request for a new branch,
an agent runs `pr-loop open`, which pushes the unsigned commits through the
GitHub API with your `gh` login and opens a draft. It refuses a default
branch, a branch GitHub already has, and any base but the default branch or
the head of another open pull request of yours in the same repository, runs
`git-push-guard`'s check, points the branch's upstream at its copy on
GitHub, and puts a pull request based on another into that one's GitHub
stack.
`pr-loop link BOTTOM ... TOP` stacks pull requests whose bases already
chain, such as those opened after a signed push, and `pr-loop stack PR`
shows GitHub's stack beside the chain of bases. When the work is done, see
what will be signed and sign it, in a terminal of your own:

``` sh
git log --format='%h %G? %s' origin/main..HEAD   # N marks an unsigned commit
git sign-pass                                     # squash fixups and sign; nothing is pushed
git sign-pass --push                              # then push every branch that moved to its own name upstream
git sign-pass --set-upstream                      # and publish moved branches that have none yet
git sign-pass --push TOP1 TOP2 ...                # several stacks, wherever they are checked out
```

`git sign-pass` re-signs from the oldest unsigned commit, or the oldest one a
`fixup!`, `amend!` or `squash!` commit folds into, to the tip, one touch per
commit in that range, folds included, since git signs each step of a squash.
Branches stacked inside that range move with it, including one checked out in
another worktree, which must be clean: the pass detaches that worktree and
switches it back afterwards. Each lands on the signed copy of the last of its
commits that squashing keeps, so a branch whose tip is a fixup lands on the
nearest commit below the tip that remains, and the pass names before any
touch each one that gains fixups from commits it does not contain. It
squashes unsigned in a scratch worktree first, so a conflicting fixup is
named before any touch, and it refuses if a commit's files, author or message
would change, or, when squashing, if the files at the tip would or the
remaining commits would differ from what git's autosquash should leave: an
`amend!` (from `--fixup=amend:` or `--fixup=reword:`) replaces its target's
whole message, subject included, and a `squash!` appends its body. No editor
opens. Merges are refused. The unsigned tip is kept at
`refs/sign-pass/backup/<branch>`.

At a missed touch in a terminal it asks: Enter retries, `q` stops. Without
one (an agent, or a `!` run) it retries that step once by itself after
`--retry-wait`, in a process group of its own, and kills that group, git and
the programs git started in it, after `--retry-timeout`, at an interrupt or
a hangup, or when the pass itself is killed, since one missed touch locks
the key and the next request then waits on pinentry without end. A killed
commit is put back, keeping the signatures before it, unless it is the
first. A refusal in a fold of `fixup!`, `amend!` or `squash!` commits, or a
killed first commit, undoes the rebase instead, and `git sign-pass --resume`
signs from the start. Either way it says how many touches carrying on
needs. The automatic retry runs outside the terminal's foreground group, so
a signer that asks for a PIN on the terminal (pinentry-tty or -curses) when
standard input is not one cannot get it there: unlock the key first, or run
the pass with standard input on the terminal. `--resume` never changes a
rebase in progress: it says whether the rebase is one the pass left stopped
and what to run, and refuses while a git the pass started may still be
running. If it stops with a rebase in progress, run
`git rebase --continue`, touching the key when it flashes, then
`git sign-pass --resume`; unlock the key first if it asks for its PIN.

When standard output is not a terminal (an agent's run, or a `!` run) the
output is brief: one line of what it will sign, one telling you to touch the
key, a progress line every 25 signatures, one line per push, and the JSON
summary; git's own output is shown only if a command fails. `--brief` and
`--no-brief` force either, so a `!` run in the session is fine. Pass `--base REF` when the
branch does not start from `origin`'s default branch or the local `main` or
`master`.

## the sandbox in every session

Every Claude Code session runs its shell commands in Claude Code's sandbox
(`sandbox` in `wgn.home.claude.settings`), and the model cannot ask to rerun
one outside it. Hooks, MCP servers and the file tools run outside it.

- **Reads.** Everything but the credential stores: `~/.ssh`, `~/.gnupg`,
  `~/.config/gh`, the login keychain, sops and age keys and sops-nix's
  decrypted secrets, ngrok's and 1Password's configs, `~/.aws`, Docker's,
  npm's and Cargo's logins, `~/.netrc`, agents' logins, `.env` files under
  `~/git` and the agents' worktrees, browser profiles, shell histories and
  the notes deploy key. The credentials are one list,
  `wgn.home.llm.guard.credentialPaths`, which the guard refuses to every
  command and file tool in Claude Code and Codex alike, with any `.env`
  file by its name. Credential variables (`GH_TOKEN`, `SSH_AUTH_SOCK`, the
  ngrok and API keys) are taken out of every command's environment.
- **Writes.** The session's working directory and temporary directory, the
  agents' worktrees, Go's and golangci-lint's caches, the Nix and uv caches
  and, on macOS, the Trash. Never the plans directory, the notes
  repository's `.git` or the review records under
  `~/.local/state/agent-tools/reviews`, which file tools are denied too.
- **Network.** Only GitHub, the Nix caches, the Go, npm, PyPI and crates
  registries and ngrok's release hosts, and on macOS the Nix daemon's
  socket. Services listening on loopback stay reachable; the comment on
  `allowLocalBinding` in `modules/home-manager/claude/default.nix` lists
  each, and whether a token closes it.

So in a session `git push`, `gh` writes and SSH fail rather than ask. The
way to GitHub is `pr-loop`: `pr-loop open` for a new pull request, `pr-loop
push`, `pr-loop reply`.

A few commands run outside the sandbox, as the operator, after the usual
permission check, and only when the whole call is that one command, with no
redirection, `cd` or command substitution:

- `gh-read`, a `gh` that runs only pull request, workflow run and
  repository reads, and refuses `--web`, which opens a browser.
- `git delegate`, whose Codex sandbox cannot start inside this one, which
  fetches pull requests with your login and writes the review records the
  sandbox denies. Every one of its subcommands is one an agent may run.
- `pr-loop`'s GitHub commands (`open`, `stack`, `link`, `push`, `threads`,
  `reply`, `resolve`, `draft`, `post`, `request`, `status`, `ci`, `logs`,
  `wait`, `sign-check`, `end`).
- `agent-plan`'s writes (`new`, `log`, `ask`, `decide`, `followup`,
  `reference`, `postpone`, `set`, `own`, `sign-id`, `sign-result`, `step`,
  `part`, `edit`, `after`, `section`): the sandbox denies the plans
  directory to every other command, so a plan changes only through them.
  `agent-plan sync`, `init` and `reseal` stay sandboxed.

They are safe to run as the operator because none runs a program its
arguments name, and `gh-read` only reads. The guard refuses a file argument
that names a credential, and `pr-loop` refuses to post text that holds a
secret's shape. The read-only ones and `agent-plan`'s writes are allowed
without a prompt.

## reviews: fresh eyes and tests that fail

Every agent session that commits a change runs the `review-change` skill's
pipeline unless you opt out for the task: a plan, a design review of it, the
work, a fresh review loop of up to three rounds, and a design review of the
result. The everyday general review is Claude Code's `/code-review`, given
the range's base; `git delegate` is the second opinion, from Codex, and
runs each focused review the change needs. A coordinator reviews each part
with an `adversarial-reviewer` sub-agent instead. Two commands give the
reviews evidence an agent cannot talk its way around.

### `git delegate`

Reviews a range, or someone else's pull request, with reviewers that did
not write it and see only the commits and what they are handed: a goal
statement (the goal, and what is not the change's job) and optionally the
items the change answers. It keeps what they found in a review record:

``` sh
git delegate --goal goal.md origin/main..HEAD          # Codex, and each focused review the change needs
git delegate --reviewer security --goal goal.md origin/main..HEAD
git delegate --reviewer compat --goal goal.md --description pr.md origin/main..HEAD
git delegate wesnel/emacs-config#2                     # someone else's pull request
git delegate show wesnel/emacs-config#2                # the review record, as JSON
git delegate settle origin/main..HEAD F3 rebut --reason-file why.md
```

The general reviewer is Codex, a model family other than the author's, run
as `codex exec --ignore-user-config --ephemeral` in a throwaway worktree of
the range's tip. None of your Codex configuration loads, so no MCP server or
plugin; web search and the image viewer are off; and a permissions profile
lets its commands read only the worktree, the parts of the git directory git
needs, the codex executable, the Nix store and the system files they need,
with no writes and no network. Codex's sandbox cannot start inside another
macOS sandbox; delegate checks that it starts, confined, before the review.
Its contract is `overlays/agent-tools/internal/review/reviewer.md`, the same
text the prompt for a sub-agent carries, and its reply must fit
`findings.schema.json` beside it.

The focused reviews (`--reviewer security`; with no `--reviewer`, those the
change's scan routes it to) are Claude Code's reviewer agents, run headless:
`claude -p --setting-sources ""` with an empty MCP config, every CLAUDE.md
kept out, their agents from the store's `review-agents.json` and their
sandbox from `reviewer-settings.json`, so no settings, hooks, agents or MCP
servers of yours or of the reviewed project reach them. Each reply is held
to its schema, and asked for once more where it does not fit. Findings that
point at no line at the tip are dropped with the reason. Every review ends
with the `review-guide` agent's walkthrough: the change's hunks in chunks,
in the order to read them, each hunk cited at the tip, and every hunk no
chunk cites listed as dropped.

The compat review (`--reviewer compat`) reviews what outlives the commit:
stored state, flags, output other programs parse, config keys and options,
and what another host or branch reads. It checks the pull request
description's `## Reverting` section against the code, from
`--description FILE` or a pull request's own, and does not run without one.

The upgrade review (`--reviewer upgrade`) reviews a change's version bumps
(lock files, Nix pins, the actions CI runs) against what changed upstream
between each pin's two versions. delegate gathers that text on the host
before the reviewer starts, over HTTPS and never over SSH: from a local
clone under `~/git/github.com`, the hash-checked source trees, the public
module proxy and GitHub's API, with private modules
(`wgn.home.llm.review.privateModules`) read only from a clone or GitHub. A
GitHub repository is read only where GitHub says it is public, or, for
your own change (a range of your repository, or a pull request you
opened), where those patterns name it. Someone else's pull request gets
public repositories alone, so it cannot point the review at private ones
your login reaches, those patterns' included. A pin nothing could be gathered for, or
that the reply does not say it read, is a finding of the review's own,
`UP-…`, settled like any other.

Every reviewer gets a worktree of its own under
`$XDG_CACHE_HOME/pr-review/OWNER/NAME/work/`, made without your git config,
hooks, LFS filter or submodules, and removed after it. A pull request is
fetched over HTTPS, pinned to the head GitHub reports. Reviewed from your
clone of its repository, it is fetched into that clone as
`refs/pr-review/N/head` and `base` and read through a worktree with nothing
checked out under `$XDG_CACHE_HOME/pr-review/OWNER/NAME/N/`, so the clone's
agent settings apply; the worktree goes when the review ends, and the refs
stay until you delete them. From anywhere else, or from a clone whose config
names a program, it goes into a bare clone of its own there, with no agent
settings. Either way it is reviewed at `merge-base..head`: the goal is ours, review it for the
operator and propose comments only, and its title, description, linked
issues and review threads go in as the author's claims. Nothing is written
to GitHub.

The record of a range is `$XDG_STATE_HOME/agent-tools/reviews/OWNER/NAME/BASE..HEAD.org`,
a round per reviewer per run, and a pull request's threads and drafted
replies are `pr-N.org` beside it, which `pr-loop threads` writes too. Only
delegate and pr-loop write them: the sandbox denies the directory to every
command, and Claude Code's file tools are denied it. Emacs's review-guide
reads them. delegate prints JSON and exits 0 with no open findings, 3 with
open findings, 4 when a reviewer could not run (Codex not installed or
signed in, its sandbox cannot start, called from inside Codex, or in a
review-loop session for the Codex reviewer; claude or its
config missing; a reply that did not fit twice), and 5 when a record fails
its structure check, which only a hand edit makes. `--print-prompt` prints a
reviewer's prompt instead of running it. See `git delegate -h`.

`git delegate ask --question FILE PATH...` asks Codex, confined the same
way, for a second opinion on a design question, from copies of the named
files alone.

It never runs `git red-check`, which runs the commits' code: the session runs
it, as it runs any test, and passes its JSON with `--red-check FILE`, a
file under the working directory. delegate refuses JSON whose commits are
not the range's, or that ran other test settings than the repository's,
records the round's red-check as the session's, and raises an `untested`
finding of its own, `RC-…`, for each commit whose tests pass without it.
Where the repository has test settings and no `--red-check` is given, it
says on stderr that the review has no red-check results. For someone else's
pull request, the session fetches it from delegate's clone, a local read,
and runs red-check on it there.

### `git red-check`

Checks that the tests a commit adds or changes test it, rather than taking
an agent's word that they failed first. In a throwaway worktree under
`.git/red-check/`, removed afterwards, it runs the test command at the
commit's parent plus only the commit's test files, which must fail, then at
the commit, which must pass:

``` sh
git red-check                                  # HEAD
git red-check --fixups main..HEAD              # each fixup on the branch
git red-check --test 'go test ./...' --build 'go vet ./...' --test-files '**/*_test.go' HEAD~2
```

It prints each commit's verdict, why it falls short, and the end of each
run's output as JSON, and exits 0 when every commit's tests build and fail
without the change and pass with it, 3 when they pass without it or fail
with it, and 4 when the evidence is missing or weak: no test files changed,
only test files changed, or the tests fail without the change only to build,
or with no build command to say whether they built. So a repository needs a
build command for any commit to exit 0. See `git red-check -h` (git turns
`--help` into a manual-page lookup).

Settings missing from the flags come from `test`, `build` and `testFiles` in
the clone's agent settings, never from the repository. Those are a JSON
object in `.git/info/agents.json` under the clone's common git directory
(`git rev-parse --git-common-dir`), shared by all its worktrees and never
tracked, with the keys `setup`, `test`, `build`, `testFiles`, `routes` and
`threatNotes` that `internal/repos` describes. The operator writes it; the
guard refuses every agent's write, and a clone without it has no settings,
so `git red-check` there runs nothing until given flags. The test command
runs the commit's code, so run it in a sandbox when the commit is not
trusted.

## plans: one file per change, one log

Agents keep each change's plan with `agent-plan`, as the `plan-work` skill
says, as files side by side in `wgn.home.llm.plans.dir`
(`~/git/github.com/wesnel/notes/agents/plans`): `<date>-<slug>.<host>.org`, a
`<date>-<slug>.<host>.<part>.org` per sub-agent's part, and
`<date>-<slug>.<host>.log.org`, the append-only record of each item's
progress and review outcomes. The logs' lock files stay out of git
through the directory's `.gitignore`, which `agent-plan` writes if it is
missing.

``` sh
agent-plan find --branch BRANCH          # plans for a branch on this host
agent-plan status PLAN                   # what is done, verified and left, and drift, as Org
agent-plan questions PLAN --all          # open questions, each with its recommendation
```

Ask an agent only to plan, and it writes the plan and stops.

`~/git/github.com/wesnel/notes` is a clone of the private `wesnel/notes`,
which activation (`agent-plan init`) clones where it is missing. On every
switch, init also points the clone's `origin` at `plans.remote` and sets it
never to sign; it sets no `core.sshCommand`, with which `git sign-pass`
refuses a repository. Its `agents/plans/` directory
holds the plans, which only `agent-plan` writes; the rest of `agents/` holds
notes agents write with the file tools, beside the operator's own org-roam
notes. A few seconds after a plan changes, and every 5 minutes besides, a
watcher (a launchd agent on macOS, a systemd path unit and timer on Linux)
runs `agent-plan sync`: it first takes the other hosts' plans by fast-forwarding the
checked-out branch to the remote's. Then, only if HEAD holds the remote's
branch, it commits this host's plan files, unsigned, as "Sync plans from
HOST", on top of the remote's branch, building the commit in an index of
its own, pushes that commit, and fast-forwards again. Sync authors
the commit as the notes clone's `user.name` and `user.email`, so GitHub
links it to the operator's account, and falls back to
`agent-plan sync <agent-plan@HOST.invalid>` as author where git has no
`user.email`. It commits as `agent-plan sync <agent-plan@HOST.invalid>`, not
as `user.email`, so `git-push-guard`, which judges the committer, lets its
unsigned commits through. It
never rebases or commits on the checked-out branch, so no commit of
the operator's is sent or re-committed. It stages only this host's plan
files that the working tree already holds as the remote does, so that git
fast-forwards over them, and only while nothing at a plan path is staged
and no merge, rebase, cherry-pick, revert or bisect is in progress. Where
the branch cannot fast-forward (it holds commits the remote lacks, another
branch or an older commit is checked out, or the index or uncommitted
edits are in the way), it stays where it is, sync says why, and sync sends
nothing while HEAD lacks the remote's branch, whose plan files may be older
than the remote's. The exception is a plan file written after sync pushed
it, which git will not fast-forward over: if the commits the branch lacks
change nothing but this host's plan files, nothing is staged, the branch
holds no commit the remote lacks and nothing is in progress, sync moves the
branch and the index to the remote's, as `git reset` does, leaves the
working tree, whose plan files are then as sent or newer, and sends them.
It holds the index's lock meanwhile, and changes nothing if the branch
moved. Otherwise bringing the branch level with the remote is the
operator's. Sync's log is `/tmp/agent-plan-sync.log` on macOS and
`journalctl --user -u agent-plan-sync` on Linux. Agents never sync: the
guard denies `agent-plan sync`, and `agent-plan reseal PLAN`, which accepts
a log edited by hand.

git reaches the repository as `git@github-notes:wesnel/notes.git` through
its own ssh config, `~/.config/agent-tools/notes-ssh-config`, whose only key
is a deploy key installed from the `notes-deploy-key` secret at
`~/.ssh/id_notes_deploy`. Sync passes that config to each command; on a
host with `programs.ssh` enabled, `~/.ssh/config` includes it, so a plain
`git push` in the clone reaches `github-notes` too. Until `secrets/wgn.yaml` holds that secret, the
secret is not declared and activation says the key is missing. To set it
up once:

``` sh
gh repo create wesnel/notes --private
d=$(mktemp -d)
ssh-keygen -q -t ed25519 -N '' -C notes-deploy -f "$d/key"
gh repo deploy-key add "$d/key.pub" --repo wesnel/notes --title agent-plan-sync --allow-write
sops set secrets/wgn.yaml '["notes-deploy-key"]' "$(jq -Rs . < "$d/key")"
trash "$d"
```

## status: what waits on you

``` sh
plan-dash export                         # writes $XDG_STATE_HOME/agent-plans/out/
plan-dash watch                          # prints a line when the session has work
plan-dash page                           # writes the page for the session to publish
```

`plan-dash export` writes the documents the "Agent plans" page keeps in
its claude.ai database, laid out as an `ArtifactData` read's `out_dir`
lays them: `plans/<plan>.json` for every host's plans that are not halted,
`github/status.json` (your open pull requests, with draft, CI, review
decision and unresolved threads, and the reviews asked of you or your
teams, with since when, the head and its CI and whether `git delegate`
holds a review record of it, all with titles, and which listed pull request each
is stacked on: the one whose head branch, in the same repository, is its
base; branch names themselves are never kept), `meta/sync.json` (when
this host's plans last reached the notes remote, whether they changed
since, when the repository last fetched, how many plans could not be
read) and `today/digest.json` (the page's Today: the demo digest of the
newest day of the last week that has one, read whole from
`digests/YYYY/MM-DD-demo.org` in the notes repository, never through a
link; where it cannot be read, the last document written stays). `gh` is
asked at most once a minute; the answer is cached as
`$XDG_STATE_HOME/plan-dash/github.json`, which holds pull request titles,
and when a fetch fails the last good answer is written, marked stale.
These leave the machine. Each is a fixed struct in `internal/export` whose
JSON tags are the allowlist, and its sentinel test fails on any other key.
Plan text comes only from the plans directory: the title, the Goal
section, the step headings under Steps, the open and decided questions
with their `RECOMMENDATION`, `ANSWER` or `LOGBOOK` note and list items,
and `STATUS`; of the log, only event kinds and times. Text
is stripped of control and formatting characters, has known secret shapes
redacted and is capped in length, and a plan's lists are capped, so a
document stays under 600 KiB. Redaction knows shapes, not data, so
customer data stays out of plans. A plan that cannot be read is named and
its document kept, and the command exits 1; only a plan gone from the
directory, or halted, loses its document. Every document on the page
carries `schema`, the version in `internal/agentplans`: fields are only
ever added, each addition raising it, so a reader uses a field only where
it is present.

`plan-dash watch` is what wakes the dashboard session, through Monitor.
Every 30 seconds it exports the plans and their sync state in memory and
prints `plans changed` when that export's hash differs from the one it
last printed for, at most once every 90 seconds. The hash leaves out what
changes on every logged event or sync (a plan's `updated`, the times of
the last push and fetch), so an agent logging its work does not cost the
session a turn. It also prints `plan sync stalled` once per stall (this
host's plans unsynced, its last push over 6 hours old), and `demo digest
ready` for a new demo digest under `digests/YYYY/` in the notes
repository. Every 2 minutes it takes the other hosts' plans
with the fast-forward-only pull, never the rebasing one. Every 5 minutes it
reads GitHub through the export's cache, which fetches at most once a
minute, and prints `reviews changed` where the pull requests the page lists
changed, and every 30 seconds it looks at their review records, printing
`review record changed` where one appeared or changed. It runs one at a
time (`already watching` otherwise) and keeps what it printed in
`watch.json`, so the watch the session starts after Monitor's 30 minutes
prints only what changed since.

## the Agent plans session

`wgn.home.claude.agentPlans.enable` (the devbox) runs the session as
`agent-plans.service`, `claude remote-control --spawn session
--permission-mode dontAsk` in `$XDG_STATE_HOME/agent-plans/session`, with
its own Claude Code config, `CLAUDE_CONFIG_DIR` at
`$XDG_STATE_HOME/agent-plans/claude`. Before the first start the operator
logs that config in and trusts the folder once: in the folder,
`CLAUDE_CONFIG_DIR=~/.local/state/agent-plans/claude claude`, `/login`,
accept the folder, quit. The unit restarts it whenever it ends, and
sends its output, a status line redrawn every second, to null.

The phone reaches the session outside tool calls too: it can run
`/config key=value`, which writes the user settings, and @-mention a
file, which Claude Code reads into the conversation without a hook. So
the session's user settings are a read-only link into the store, where
`/config` fails, and never the operator's `~/.claude`; and they deny
reads of credentials and the notes deploy key, which Read rules hold
@-mentions to. They hold the baseline guard's hook with `onFailure:
block` (Claude Code 2.1.295 or later), so a hook that cannot run denies,
and allow the skill's tools: Bash `plan-dash export`, `plan-dash page`
and `plan-dash page published`, Artifact, Monitor, ArtifactData, Agent,
Skill, PushNotification and ToolSearch; and a SessionStart hook,
`plan-dash prime`, that names the page and the `agent-plans` skill after
every start, `/clear` and `/compact`. The
session folder is writable by any process running as the operator, and a
settings file there could switch the hook off; only managed settings,
which need root, would be a wall. Any Remote Control session, not only
this one, takes `/config` and @-mentions from the phone with the
operator's account. The skill, in the same config, arms Monitor on
`plan-dash watch`, again on every expiry, and runs each update in a
foreground sub-agent: export, then write what it wrote to the page,
version-pinned, reading the page only into files so no document's text
enters the conversation. A stall or a demo digest becomes a
PushNotification with fixed text, and a digest also an update, which puts
it in the page's Today. The kill switch is its own: create
`$XDG_STATE_HOME/agent-plans/off`, then `systemctl --user stop
agent-plans.service`.

Its page is named in `agent-tools/agent-plans.json` under the XDG config
directory, `{"url": "https://claude.ai/artifact/ID"}`, which the operator
writes once on the host; the URL stays out of this repository. Started
without it, the session publishes the page, pinned, and gives the
operator its URL to write there before restarting the service.

The page is `overlays/agent-tools/internal/agentplans/page.html`, built
into agent-tools. `plan-dash page` writes it to
`$XDG_STATE_HOME/agent-plans/page/agent-plans.html` and says whether it
differs from the one last published, which `plan-dash page published`
records; the session republishes it when it starts after a switch that
changed it.
Every publish declares the capabilities `plan-dash page` prints, from
`internal/agentplans`: the page's database, read and written by its owner
alone, which the session writes. The page reads the documents `export`
writes and writes nothing: each open question shows its recommendation
and options as text, to answer with `/questions` in a Claude session, and
each review request its CI and whether `git delegate` holds a review
record of it. It shows how fresh its data is (live; partial, where GitHub could not
be read or a plan couldn't be exported), when the devbox last pushed and
fetched the plans, the review requests by state (ready, CI failing,
draft or reviewed), oldest first with three days or more marked, stacked
pull requests under the one they build on, the latest demo digest, and each
plan at `#<plan ID>`. A pull request the devbox's `git delegate` has
reviewed shows its review record's counts (findings by outcome, threads,
drafts to post) and up to five of its diagrams, never its titles or other
model text; a diagram's labels are redacted, and Mermaid, loaded by its
hash, draws it in a sandboxed frame with no access to the page, which shows
the drawing only as an image. Every status mark has a word as well as a colour,
and all text from the documents is set as text, never parsed as markup,
and looked up in the page's tables by own key only, so a STATUS such as
`constructor` falls back to its own label; its page test holds it to
that, to the capabilities, to the collections it reads, to making no
write and to the fields of each document. It uses Mantle's colours in
the system face and loads no file.

## pushing: private history stays private

`wgn.home.git` runs `git-push-guard` before every push from every
repository, next to the repository's own pre-push hook. It refuses a push to
a public GitHub repository when a commit it sends is reachable from a
remote-tracking ref of a private GitHub repository that one of the checkout's
remotes fetches from: for example a private repository's `main` pushed to a
public one, whether through a stray remote, a push URL or a URL typed out.
Commits the target or any other public remote already has are not counted, so
a private fork can still push upstream's own history and new work on top of
it. It sees history only, not private code copied in as new commits.

Each push to GitHub looks up the visibility of the target and of the
checkout's other GitHub remotes with your `gh` login, and is refused when
that fails. To publish private history in one public repository on purpose:

``` sh
git -c push-guard.allowPublic=OWNER/NAME push ...
```

The guard (see [the guard in every session](#the-guard-in-every-session))
denies an agent setting `push-guard.*`.

## pushing: your commits go out signed

`git-push-guard` also refuses a push that publishes a commit committed under
the checkout's `user.email` with no signature or a bad one, naming the
commits; sign them with `git sign-pass` and push again. Only commits the
remote lacks count: not those the pushed refs already point at, nor those
reachable from the remote-tracking refs of a remote that fetches from or
pushes to the URL. Other people's commits never count, and a push to a path
on this machine publishes nothing. A commit signed in a way this machine
cannot verify passes; checking the rest needs no key touch, and a check that
takes over 30 seconds refuses the push. To publish unsigned commits on
purpose, for one push:

``` sh
git -c push-guard.allowUnsigned=true push ...
```

`pr-loop`'s pushes go through the GitHub API, which runs no hook, so its
unsigned commits are left for the signing pass.

## the review loop

An agent drives a stack of your open pull requests through CI and review,
in one of two modes you choose when you start it:

- **bots:** it fixes CI failures and the review bots' comments as fixups,
  pushes, answers in each bot's threads, and asks the bots in
  `wgn.home.llm.prLoop.bots` (Copilot and Qodo by default) to review every
  new head, drafts included, once CI is green.
- **people:** it triages the threads people started, commits the fixes as
  fixups and drafts each answer into the PR's review record. It pushes and
  posts nothing: you do both.

### starting a loop

You need `gh auth login`, which `pr-loop` uses outside the sandbox, and open
PRs by you, draft or ready, in one repository, stacked by base branch. On a
ready PR, the loop's pushes, replies and review requests notify its
reviewers and code owners. Then, in the repository:

``` sh
review-loop --harness codex --bots 12 13     # or --harness claude, or --people
```

It starts an ordinary session in the selected harness, with the host sandbox and the
guard every session has, running the `review-loop` skill on those PRs. Each
round waits for CI, asks for reviews, triages the threads, fixes in fixups,
has a fresh review check them (see
[reviews](#reviews-fresh-eyes-and-tests-that-fail); in a loop the second
opinion uses Claude, through a sub-agent in Claude Code or
`git delegate --reviewer claude` in Codex), restacks, and in bots mode pushes the stack through
the GitHub API, then replies and resolves. It stops when the bots are
satisfied or the loop needs a decision, and hands back.

A repository whose fresh checkouts need preparing, such as generating files
it does not commit, gets a `setup` command in each clone's agent settings
(see [reviews](#reviews-fresh-eyes-and-tests-that-fail)). `pr-loop setup
[PATH]` runs it in the sandbox in a worktree,
printing the command and its exit status; the loop runs it in each worktree
it makes, and nothing runs it on its own. A repository cannot name a setup
command itself.

`pr-loop` writes only to open PRs by you in the same repository, never the
default branch, and re-checks that before every write. `pr-loop push`
moves a branch only from the head your checkout last fetched, or the one
`--lease BRANCH=SHA` names, so a push made elsewhere is never overwritten,
and on a ready PR it takes only fixups of the PR's own commits. `pr-loop
reply` and `resolve` take only threads whose every comment is a configured
bot's or yours.

### after the loop

In a terminal of your own, in the repository:

``` sh
git sign-pass --push TOP   # squash the fixups, sign, and push the stack, TOP its top branch
pr-loop post 12            # people mode: post the drafted answers, each after you confirm it
```

The loop runs `pr-loop sign-check` before it hands over, which squashes the
fixups unsigned in a throwaway worktree and stops unless every PR would end
with exactly its own commits and the files the loop left. `pr-loop post`
shows each draft on your terminal and posts exactly what it showed once you
answer `y`; it resolves the threads answered fix or rebut, and once every
person's thread has an answer, asks those people to review again. A bots
loop deletes its trigger comments with `pr-loop end` as it hands back.

These stay yours: waiving a CI failure (`pr-loop request --accept-failure
CHECK`), pushing and posting in people mode, marking a PR ready for review,
merging, and any change to Linear, which the hand-back proposes instead.

### what the wrapper adds

`review-loop` sets `PR_LOOP_SESSION` and `PR_LOOP_MODE` for the selected
session and its shell commands; `pr-loop` gates its writes on the mode.
Claude Code also gets its auto-mode context and an allow rule for `pr-loop`.
Codex retains its normal approval settings, hooks and sandbox; approve
`pr-loop` commands through its normal approval flow when required. Trust
the baseline hook with `/hooks` before using a Codex loop. Both harnesses
load the shared `review-loop` skill. Independent Claude reviews require
Claude Code and its configured reviewer sandbox as well.
The guard denies an agent running `review-loop`,
setting either variable, or running `pr-loop post`.

## the guard in every session

`pr-loop guard --baseline` runs before every tool call of every session:

- Claude Code: a `PreToolUse` hook in `~/.claude/settings.json` for the shell,
  Monitor, ExitWorktree, the file tools and MCP tools. `Read` is denied the credential files
  through `permissions.deny` instead, made from the same list as the
  guard's, `wgn.home.llm.guard.credentialPaths`. In auto mode the classifier
  also holds every session to `autoMode.hard_deny` rules that restate the
  guard's hardest lines in prose.
- Codex: `~/.codex/hooks.json`, for every tool but `write_stdin`, which
  runs no hook; so the guard refuses to start a command that only reads
  the terminal, which `write_stdin` could then drive. Codex skips a hook
  until it is trusted, so trust it with `/hooks` once after the first switch.

It denies what is yours to do: pushing (hand over `git sign-pass --push`,
or open a draft with `pr-loop open`), anything else that waits for a key
touch (`git sign-pass`, a fetch, pull, clone or ls-remote whose URL is SSH
after `insteadOf`, and ssh, scp or sftp to github.com or git.sr.ht),
merging, closing, marking ready,
reviews and top-level comments, GitHub settings, gh's login and token, git
credential and transport settings, remote rewrites, settings every worktree
shares, defining a git alias that runs a shell command (`!…`), adding a
git config include or writing a git config file other than with `git
config`, discarding the main checkout's work, dropping or
clearing the stash every worktree shares, deleting unpushed history or a worktree's
uncommitted work, reading credentials or the whole environment, ngrok API writes, running
with `NGROK_ENV` set to prod or stage however it is set, switching
the system, root, publishing packages, killing Emacs, gpg-agent, ssh,
nix-daemon or mcp-proxy, killing by name or pattern (`pkill`, `killall`)
or any PID the session's own commands did not start or that is not
written as a number (`kill $(pgrep …)`, `xargs kill`), stopping or
restarting an Emacs or deleting, starting or stopping its server other
than a test one by its own socket, sending a test one Lisp that names
another server, destroying the home directory, the Nix store or
your keys, switching its own hooks off, changing agent-tools' config,
sleeping 30 seconds or more in the
foreground, and writing a plan's log other than with `agent-plan log`.
MCP tools of the servers in `overlays/agent-tools/internal/guard/mcp.json`
(Linear, Notion, Hex, Datadog, Buildkite, ngrok, and the
claude.ai Slack connector) are held to its lists: reads
pass, the listed writes ask you first in Claude Code and are denied in
Codex and to reviewers, and any other tool is denied until it is
listed. Every tool of a server it does not list asks you first in Claude
Code, and is denied in Codex and to reviewers. Each refusal says what to do
instead. Each reviewer agent, defined in `modules/home-manager/claude/agents`
with only Read, Bash and StructuredOutput, also runs `pr-loop guard
--reviewer` from its own definition, which keeps it to reading in any
session. The guard also denies starting a
review loop or setting its variables (`review-loop`,
`PR_LOOP_SESSION`, `PR_LOOP_MODE`) and `pr-loop post`, which are yours.

The guard reads commands as typed, through wrappers, shells and `env`, so it
catches a forgotten rule, not a determined one. The rules that need no
repository state are in `overlays/agent-tools/internal/guard/policy.json`,
each with a `scope` (`baseline` or `reviewer`) and
commands it must deny and allow, which the guard's tests hold it to, failing
on a denial whose reason does not go on to say what to do instead; the rest
are in the guard's Go tests. A call whose input the guard cannot read is
denied, and Claude Code's hooks run with `onFailure: "block"`, so a hook
that cannot run denies too.

Each rule is held by what each harness has. In Claude Code the sandbox and
permission rules hold most of them, and the hook catches the spellings they
cannot see and holds the file tools, which run outside the sandbox; Codex
has the hook alone:

| Rule | Claude Code | Codex |
|---|---|---|
| Credential reads (paths, security, sops -d, op, pass, env dumps) | sandbox denyRead and the hook, one list | hook |
| sudo, doas, su, pkexec, osascript | hook | hook |
| darwin-rebuild/nixos-rebuild/home-manager switch, nix profile/copy, nix-env | hook; sandbox (profiles not writable) | hook |
| Publishing (npm, cargo, twine, gem, docker push, goreleaser) | sandbox egress allowlist, hook, deny rules | hook |
| NGROK_ENV prod/stage in any form; ngrok API writes | hook (sandbox scrubs the tokens) | hook |
| Fetch/clone/login over SSH (touch) | sandbox (no SSH_AUTH_SOCK, ~/.ssh, port 22) and hook | hook |
| Signing commits (touch) | sandbox (~/.gnupg, agent socket) and hook | hook |
| git push, gh writes, GitHub API/GraphQL writes | sandbox (no credentials), hook, deny rules | hook |
| git-state rules (main checkout, stash, worktrees, history loss, shared config, shell aliases) | hook | hook |
| Shell edits of tracked files, rm where trash is | hook | hook |
| Guard-off writes (settings, hooks, agent-tools config) | hook; sandbox for ~/.claude | hook |
| Plan logs and files | sandbox denyWrite (Bash), Edit deny rules and hook (file tools) | hook |
| Destroying home, root, store, keys | sandbox and hook | hook |
| Kills (protected processes, patterns, others' PIDs) | hook | hook |
| Long foreground sleeps | hook | hook |
| Bypass-permissions launches | disableBypassPermissionsMode and hook | hook |
| ExitWorktree discarding work | hook | (no such tool) |
| Ownership fence | hook (file tools and shell) | hook |
| Reviewer read-only | agent tools (Read, Bash, StructuredOutput) and the frontmatter hook | (no role agents) |
| MCP reads/confirms; unlisted tools refused | hook, permission rules and mcp-proxy's filter | hook and mcp-proxy's filter |
| Terminal readers | (n/a) | hook |
| Emacs (forms named as written; Lisp that builds a form's name at run time is not read) | hook | hook |

git itself does not use the keychain's GitHub login:
`credential."https://github.com".helper` is empty in `wgn.home.git`.

## MCP servers

`wgn.home.mcp-proxy.enable` runs one MCP proxy for every agent at
`http://127.0.0.1:6290/<name>/mcp`:

| Name | Upstream | Access |
| --- | --- | --- |
| `linear` | Linear's `/mcp` | reads; writes you confirm, OAuth |
| `buildkite` | Buildkite's `/mcp/readonly` | read tools only, OAuth |
| `notion` (`notion`) | Notion's `/mcp` | reads; writes you confirm, OAuth |
| `hex` (`hex`) | Hex's `/mcp` | reads; edits and runs you confirm, OAuth |
| `datadog` (`datadog.enable`, `datadog.site`) | the site's `/v1/mcp`, toolsets `core` and `error-tracking` | read tools only, OAuth |

Options in parentheses are off by default. Each remote server serves only
the tools listed for it in the guard's `mcp.json`. In Claude Code those
lists are also permission rules generated from `mcp.json`: each server's
reads allowed and its listed writes asked, and the guard still refuses any
other tool on a listed server. Codex's lists are the guard's alone, and
Codex and reviewers get the reads alone.

The proxy signs in to each remote server itself and refreshes their
short-lived tokens, so agents hold only the proxy's own token, kept in
`~/.local/state/mcp-proxy/token`. The servers are registered with Claude
Code and Codex on every switch, and each reads the token when it
connects.

Authorize each server once, in a browser, after the first switch:

``` sh
mcp-proxy-authorize linear
mcp-proxy-authorize buildkite
mcp-proxy-status
```

A session started while a server was not authorized reports it as not
found; reconnect it with `/mcp` in Claude Code, or start a new session.
The proxy serves only the servers it connected, so a server whose token
was refused is not found either. `pr-loop mcp-status` names each server
that is not connected and the command that connects it; each Claude Code
session runs it as it starts, so the session can ask you rather than add
the vendor's server itself. If one does add it, the guard's reason for a
server named after a reviewed service names the reviewed server.

The browser is sent back to `http://localhost:8090/oauth/callback` on the
machine running the proxy. On one without a browser, such as the devbox,
authorize over SSH with that port forwarded, and open the URL it logs on
the machine running SSH:

``` sh
ssh -L 8090:localhost:8090 devbox mcp-proxy-authorize linear
```

Run `mcp-proxy-authorize` again if a server's token stops being accepted.
The proxy logs to `~/.local/state/mcp-proxy.log`.

GitHub is read through `gh`. ngrok's account server is registered in Claude
Code only, signed in from there, and the guard lets through only tools
whose names say they read. Claude's claude.ai connectors belong to the
account rather than this configuration: disconnect them in claude.ai's
settings to keep Claude to the proxy.

# replacing the devbox

A replacement devbox arrives as an Ubuntu host with Nix already on it, reachable
as `wesley` with UID 1000. The home-manager generation is carried over by
deploying it, but the following are not.

`devbox-host` holds a bare IPv4 address, which the replacement will not reuse:

``` bash
nix-shell --run "sops secrets/wgn.yaml"
```

The `Host devbox` block is rendered from that secret during activation, so ngrok
has to be rebuilt before `devbox` resolves anywhere new:

``` bash
darwin-rebuild switch --flake '.#ngrok'
ssh-keygen -R <previous address>
```

Permit the forwarded GnuPG agent sockets to replace the ones already bound on
the host. The `RemoteForward` lines in the ngrok machine depend on this, and it
is not stored anywhere Nix manages:

``` bash
echo 'StreamLocalBindUnlink yes' | sudo tee /etc/ssh/sshd_config.d/99-streamlocal-bind-unlink.conf
sudo sshd -t && sudo systemctl reload ssh.service
```

Then start the forward, touching the YubiKey when it flashes:

``` bash
launchctl kickstart -k gui/$(id -u)/org.nixos.devbox-agent-forward
```

`ssh devbox "ssh-add -l"` naming the card means the agent is through and the
deploy can decrypt. Note that `gpg --card-status` on the devbox answers
`Forbidden` even when everything is working, because the socket it reaches is
ngrok's restricted one.

# updating systems

## nixOS

Assuming you're in this directory:

```bash
sudo nixos-rebuild switch --flake '.#framework'
```

## orbstack nixOS virtual machine

Assuming you're in this directory:

``` bash
sudo nixos-rebuild switch --flake '.#orb' --impure
```

This is intended to be used in an [OrbStack](https://orbstack.dev) virtual machine running NixOS.

## nix-darwin

Assuming you're in this directory:

```bash
darwin-rebuild switch --flake '.#artifact'
```

## home-manager

Assuming you're in this directory:

```bash
home-manager switch --flake '.#artifact'
```

## devbox

devbox runs Ubuntu rather than nixOS, so only its home-manager generation is
deployed, and it is pushed from another machine rather than switched in place.
From the dev shell in this directory:

``` bash
deploy --skip-checks '.#devbox'
```

The `devbox` host alias is written into `~/.ssh/config` by the ngrok machine, so
this only resolves where that machine's sops secrets are decrypted.

`--skip-checks` is required from a darwin machine. It suppresses one thing,
deploy-rs's `nix flake check`, which covers every machine in the flake rather
than the one being deployed; devbox is still evaluated and built. That check
fails here because evaluating the x230 configuration builds its Emacs
configuration, and that build is `x86_64-linux`:

```
error: Cannot build '...-emacs-treesit-grammars.drv'.
       Reason: platform mismatch
       Required system: 'x86_64-linux'
       Current system: 'aarch64-darwin'
```

Passing `--no-build` instead does not help, and cannot be aimed at the check
alone: deploy-rs forwards trailing arguments to its build and eval commands
too, and `nix build` rejects that flag. Deploying from an `x86_64-linux`
machine needs no such flag.

# troubleshooting

## nixOS

### stale lockfiles in `.gnupg/public-keys.d/` cause gpg to hang

Remove `use_keyboxd` from `.gnupg/common.conf`. This file seems to be a rogue file created by GPG, rather than one managed by Nix.

### error with `command-not-found` helper function

```
DBI connect('dbname=/nix/var/nix/profiles/per-user/root/channels/nixos/programs.sqlite','',...) failed: unable to open database file at /run/current-system/sw/bin/command-not-found line 13.
cannot open database `/nix/var/nix/profiles/per-user/root/channels/nixos/programs.sqlite' at /run/current-system/sw/bin/command-not-found line 13.
```

The fix is to run `sudo nix-channel --update` to update the channel that this command uses to find software.

## macOS

### `$PATH` gets mangled

On MacOS, there is an `/etc/paths` file and an `/etc/paths.d` directory, which a tool called `path_helper` consults to put things on your `$PATH`. The default `/etc/profile` seems to be responsible for executing `path_helper`. This was never relevant to me or my Mac, until I noticed that everything Nix-related in my `$PATH` was suddenly moved to the end of the `$PATH`. This caused a lot of things to break, since, for example, I would end up using the `git` from `/usr/bin` rather than the one from `/etc/profiles/per-user`. I doubt the following is the "correct" way to fix this, but I seem to have resolved this issue by modifying the `/etc/paths` file using Nix.
