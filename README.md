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
    - [the bot review loop](#the-bot-review-loop)
        - [starting a loop](#starting-a-loop)
        - [after the loop](#after-the-loop)
        - [what a loop session enforces](#what-a-loop-session-enforces)
    - [MCP servers](#mcp-servers)
- [the coding assistant](#the-coding-assistant)
    - [what nix owns](#what-nix-owns)
    - [the sandbox image](#the-sandbox-image)
    - [reaching a hosted provider](#reaching-a-hosted-provider)
    - [running against a model on this machine](#running-against-a-model-on-this-machine)
    - [the documentation index](#the-documentation-index)
    - [running the sandbox on a remote host](#running-the-sandbox-on-a-remote-host)
    - [the bubblewrap backend](#the-bubblewrap-backend)
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

Claude Code (`wgn.home.claude`), Codex (`wgn.home.codex`) and ECA
(`wgn.home.eca`) share one setup. Amp and Copilot receive the shared
instructions and skills and nothing else.

## what nix sets up

Enabling any of those modules enables `wgn.home.llm`, which provides:

- **Instructions.** `modules/home-manager/llm/AGENTS.md` is linked to where
  each agent reads its user-level instructions.
- **Skills.** `wgn.home.llm.skills` are linked into each agent's skill
  directory. A skill marked `sandbox = true` is also carried into sandboxed
  sessions; the rest drive something on this machine, such as Emacs.
- **`agent-tools`** on `PATH`: `git sign-pass`, `git red-check`,
  `git fresh-review`, `pr-loop`, `sandbox-relay` and `agent-plan`, built from
  `overlays/agent-tools`. `wgn.home.git` uses
  its `git-push-guard` as well.

Agents write the files below themselves, so Nix merges its keys into them on
every switch rather than owning them. Everything else in them is left alone,
and a change to one of these keys made from inside an agent is undone by the
next switch.

| File | Keys set on every switch |
| --- | --- |
| `~/.claude/settings.json` | `wgn.home.claude.settings`: attribution off, the ngrok and reviewer hooks, and the plan hook on session resume or compaction (a hook list here replaces the file's own) |
| `~/.claude.json` | `mcpServers` entries for the MCP proxy |
| `~/.codex/config.toml` | `[mcp_servers.*]` tables for the MCP proxy |
| `~/.config/eca/config.json` | `mcpServers` entries for the MCP proxy |

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
commit. Branches stacked inside that range move with it. It squashes unsigned
in a scratch worktree first, so a conflicting fixup is named before any
touch, and it refuses if a commit's files, author or message would change, or,
when squashing, if the files at the tip would or the remaining commits would
differ from what git's autosquash should leave: an `amend!` (from
`--fixup=amend:` or `--fixup=reword:`) replaces its target's whole message,
subject included, and a `squash!` appends its body. No editor opens. Merges are refused. The unsigned
tip is kept at `refs/sign-pass/backup/<branch>`.

If it stops at a missed touch, run `git rebase --continue`, touching the key
when it flashes, then `git sign-pass --resume`. Pass `--base REF` when the
branch does not start from `origin`'s default branch or the local `main` or
`master`.

## reviews: fresh eyes and tests that fail

Every agent session that commits a change runs the `review-change` skill's
pipeline unless you opt out for the task: a plan, a design review of it, the
work, a fresh review loop of up to three rounds, and a design review of the
result. Two commands give the reviews evidence an agent cannot talk its way
around.

### `git fresh-review`

Reviews a range with a reviewer that did not write it and sees only the
commits and the files it is handed: a goal statement (the goal, and what is
not the change's job), optionally the items the change answers and earlier
rounds' findings:

``` sh
git fresh-review --goal goal.md origin/main..HEAD
git fresh-review --goal goal.md --items threads.md --prior round1.json origin/main..HEAD
```

The reviewer is Codex, a model family other than the author's, run as
`codex exec --ignore-user-config --ephemeral` in a throwaway worktree of the
range's tip under `.git/fresh-review/`, removed however the review ends.
None of your Codex configuration loads, so no MCP server or plugin; web
search and the image viewer are off; and a permissions profile lets its
commands read only the worktree, the parts of the git directory git needs,
the codex executable, the Nix store and the system files they need, with no
writes and no network. Codex's sandbox cannot start inside another macOS
sandbox; `git fresh-review` checks that it starts, confined, before the
review. Its contract is `overlays/agent-tools/internal/review/reviewer.md`,
the same text the `reviewer` agent of Claude Code and ECA loads, and its
reply must fit `findings.schema.json` beside it. Findings that point at no
line at the tip are dropped with the reason. It prints JSON and exits 0 with
no findings, 3 with findings, and 4 when Codex cannot review: not installed,
not signed in, its sandbox cannot start, called from inside Codex, or in a
review-loop session, where `pr-loop` must stay the only way out.
`--print-prompt` prints the prompt instead of running the review. See
`git fresh-review -h`.

`git fresh-review ask --question FILE PATH...` asks Codex, confined the same
way, for a second opinion on a design question, from copies of the named
files alone.

It never runs `git red-check`, which runs the commits' code: the session
runs it, as it runs any test. Where the repository has test settings, it
says on stderr that the review has no red-check results.

### someone else's pull request: `pr-loop review`

``` sh
pr-loop review wesnel/emacs-config#2                # Codex reviews it
pr-loop review --reviewer subagent 2                # prints the prompt for a reviewer sub-agent
pr-loop review --check 2 < reply.md                 # checks that sub-agent's reply
pr-loop review settle 2 < outcomes.json             # records how triage settled each finding
```

It fetches the pull request over HTTPS into a bare clone of its own under
`~/.cache/pr-review/OWNER/NAME/N/`, pinned to the head GitHub reports, and
reviews it with the same contract as `git fresh-review`. The goal is ours:
review it for the operator and propose comments only; the title, description,
linked issues and review threads go in as the author's claims. Every git
command on the clone runs without your git config, hooks, links or
submodules, so a pull request's `.gitattributes` and `.lfsconfig` run no
filter and reach no server, and Codex runs confined in a checkout beside the
clone. The record, `HEAD12/findings.json`, is what review-guide imports for
you to edit and post; nothing is written to GitHub. A `claude-pr-loop`
started in `HEAD12/` reads only that review, and may run `pr-loop review`
for its own pull request alone. See `pr-loop --help`.

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
`wgn.home.llm.repos`, keyed by `owner/name`, never from the repository. The
test command runs the commit's code, so run it in a sandbox when the commit
is not trusted.

## plans: one file per change, one log

Agents keep each change's plan with `agent-plan`, as the `plan-work` skill
says, as files side by side in `wgn.home.llm.plans.dir`
(`~/git/notes/agents`): `<date>-<slug>.<host>.org`, a
`<date>-<slug>.<host>.<part>.org` per sub-agent's part, and
`<date>-<slug>.<host>.log.org`, the append-only record of each item's
progress and review outcomes. The logs' lock files stay out of git
through the directory's `.gitignore`, which `agent-plan` writes if it is
missing.

``` sh
agent-plan find --branch BRANCH          # plans for a branch on this host
agent-plan status PLAN                   # what is done, verified and left, as Org
agent-plan questions PLAN --all          # open questions, each with its recommendation
```

Ask an agent only to plan, and it writes the plan and stops.

`~/git/notes` is a clone of the private `wesnel/notes`, which activation
clones where it is missing. A few seconds after a plan changes, a watcher (a
launchd agent on macOS, a systemd path unit on Linux) runs `agent-plan
sync`: it commits this host's plan files by name, unsigned, as "Sync plans
from HOST", rebases onto the remote with any other edits stashed and put
back, and pushes. Its log is `/tmp/agent-plan-sync.log` on macOS and
`journalctl --user -u agent-plan-sync` on Linux. Agents never sync: the
guard denies `agent-plan sync`, and `agent-plan reseal PLAN`, which accepts
a log edited by hand.

git reaches the repository as `git@github-notes:wesnel/notes.git` through
its own ssh config, `~/.config/agent-tools/notes-ssh-config`, whose only key
is a deploy key installed from the `notes-deploy-key` secret at
`~/.ssh/id_notes_deploy`. Until `secrets/wgn.yaml` holds that secret, the
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

## the bot review loop

An agent can drive a stack of open pull requests through CI and the review
bots unattended: fixing failures and review comments as fixups, answering in
each bot's threads, and stopping with a report for you to sign.

### starting a loop

You need, on this machine:

- `gh auth login`, which `pr-loop` uses on the host;
- open PRs by you, draft or ready, in one repository, stacked by base
  branch. On a ready PR, the loop's pushes, replies and review requests
  notify its reviewers and code owners;
- a clean main worktree.

Then, in the repository, start one of:

- any Claude Code session, and ask it to run the review loop on the branch.
  It starts `claude-pr-loop` headless in the background, logging to
  `.git/pr-loop-session.jsonl` and `.err`, and shows the report when the loop exits;
- `claude-pr-loop`, and ask it to run the review loop on the branch;
- `codex-pr-loop`, and ask the same. Codex skips a hook until it is trusted,
  so trust `pr-loop guard` in `/hooks` the first time, and again after a
  switch changes it;
- `M-x wgn/eca-pr-loop`, then choose the `pr-coordinator` agent.

Each runs the agent's commands in a sandbox without credentials, beside a
`sandbox-relay` that runs `pr-loop` on this machine with your `gh` login, and
`claude-pr-loop` and `codex-pr-loop` also `agent-plan`'s reading commands and
`log`, the session's only way to its plan. The
`review-loop` skill refuses to start anywhere else. On Linux, Claude Code's
sandbox needs `bubblewrap` and `socat` on `PATH`.

The coordinator gives each PR to an owner sub-agent, which works in its own
worktree under `.pr-loop/<number>`, where signing is off. Each round waits
for CI, requests reviews from the bots in `wgn.home.llm.prLoop.bots`
(Copilot and Qodo by default), triages their threads, fixes in fixups, has
one fresh review check those fixups before they go out (see
[reviews](#reviews-fresh-eyes-and-tests-that-fail); in a loop the reviewer
is always a sub-agent), restacks, pushes the stack through the GitHub API,
then replies and resolves.
It stops when the bots are satisfied or the loop needs a decision, and
writes `.git/pr-loop/report.md`.

A repository whose fresh checkouts need preparing, such as generating files
it does not commit, gets a `setup` command in `wgn.home.llm.repos`, keyed by
`owner/name`. `pr-loop worktrees` runs it in the sandbox in each worktree
whose HEAD moved since its last setup, printing the command and its exit
status, and `pr-loop setup [PATH]` runs it in any worktree, such as one an
agent made by hand. A repository cannot name a setup command itself.

`pr-loop` touches only the registered branches, each an open PR by you in
the same repository and not the default branch. It re-checks that before
every write, and accepts only fixups of each PR's own commits.

### after the loop

In a terminal of your own, in the repository:

``` sh
pr-loop sign        # squash the fixups, sign, and push the stack over SSH (--no-push to keep it local)
pr-loop end         # delete the bots' trigger comments and the loop's state
```

`pr-loop sign` refuses unless every PR ends with exactly the commits it
started with and the files the loop left. `pr-loop end --prune` also deletes
the round snapshots and backups. Until then, each round's work is reviewable
with:

``` sh
git range-diff refs/pr-loop/rounds/0/<branch>...refs/pr-loop/rounds/<n>/<branch>
```

These stay yours: waiving a CI failure (`pr-loop request --accept-failure
CHECK`), pruning, marking a PR ready for review, merging, and any change to
Linear, which the report proposes instead.

### what a loop session enforces

- **Sandbox.** Every launcher takes its rules from `wgn.home.llm.prLoop`:
  `deniedReads` (credentials, the keychain, the MCP proxy's token);
  `allowedReads` (in `claude-pr-loop` and `codex-pr-loop`, the only paths
  under your home directory its commands may read: the workspace, its git
  directory, the build cache and agent-tools' config), with `gitConfig` in
  place of your git config; `protectedPaths` (read-only in the workspace:
  git hooks and config, and agents' project settings); `allowedDomains` (the
  only network, for dependencies, without storage.googleapis.com in a
  session started under `reviewDir`) and `cacheDir` (the only build cache it
  writes).
- **Guard.** `pr-loop guard` runs before every tool call in all three agents
  and denies other ways to write to GitHub or Linear, unsigned-workflow
  violations and your decisions. Owner agents are also denied the steps the
  coordinator alone takes (`pr-loop push`, `reply` and the like) in Claude
  Code and ECA, and reviewer agents also every git and `pr-loop` command
  that changes something. ECA's agents also carry deny patterns built
  from `overlays/agent-tools/internal/guard/policy.json`. Add a rule there,
  with commands it must deny and allow: the guard's tests and the package
  build hold both to those examples, and fail on a denial whose reason does
  not go on to say what to do instead.

An ordinary session has neither the sandbox nor the loop's rules, and runs
with your credentials, but has the guard in every session.

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
- ECA: a `preToolCall` hook, `guard-baseline`, merged into
  `~/.config/eca/config.json` on every switch.

It denies what is yours to do: pushing (hand over `git sign-pass --push`,
or open a draft with `pr-loop open`), anything else that waits for a key
touch (`git sign-pass`, a fetch, pull, clone or ls-remote whose URL is SSH
after `insteadOf`, and ssh, scp or sftp to github.com or git.sr.ht),
merging, closing, marking ready,
reviews and top-level comments, GitHub settings, gh's login and token, git
credential and transport settings, remote rewrites, settings every worktree
shares, discarding the main checkout's work, dropping or clearing the stash
every worktree shares, deleting unpushed history or a worktree's
uncommitted work, reading credentials or the whole environment, ngrok API writes, running
with `NGROK_ENV` set to prod or stage however it is set, switching
the system, root, publishing packages, killing Emacs, gpg-agent, ssh,
nix-daemon or mcp-proxy, destroying the home directory, the Nix store or
your keys, switching its own hooks off, changing agent-tools' config,
sleeping 30 seconds or more in the
foreground, and writing a plan's log other than with `agent-plan log`. A write through a claude.ai
connector asks you first in Claude Code, and is denied in Codex. Each
refusal says what to do instead. An agent with a loop role (`pr-owner`,
`reviewer`, ECA's `pr-coordinator`) has that role's rules in any session.

The guard reads commands as typed, through wrappers, shells and `env`, so it
catches a forgotten rule, not a determined one. The rules ECA can also
match as patterns are in `overlays/agent-tools/internal/guard/policy.json`,
each with a `scope` (`baseline`, `coordinator`, `owner` or `reviewer`); the
rest, which need the repository's state, are in the guard's Go tests.

git itself does not use the keychain's GitHub login:
`credential."https://github.com".helper` is empty in `wgn.home.git`.

## MCP servers

`wgn.home.mcp-proxy.enable` runs one MCP proxy for every agent at
`http://127.0.0.1:6290/<name>/mcp`:

| Name | Upstream | Access |
| --- | --- | --- |
| `linear` | Linear's `/mcp/readonly` | read tools only, OAuth |
| `buildkite` | Buildkite's `/mcp/readonly` | read tools only, OAuth |
| `docs` | the documentation index | search and listing only |

The proxy signs in to Linear and Buildkite itself and refreshes their
short-lived tokens, so agents hold only the proxy's own token, kept in
`~/.local/state/mcp-proxy/token`. The servers are registered with Claude
Code, Codex and ECA on every switch, and each reads the token when it
connects. ECA sessions sandboxed by Gondolin reach the proxy at
`http://mcp-proxy:6290`, and see only a placeholder for the token; those
sandboxed by bubblewrap cannot reach it.

Authorize each server once, in a browser, after the first switch:

``` sh
mcp-proxy-authorize linear
mcp-proxy-authorize buildkite
mcp-proxy-status
```

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
Code only, signed in from there, and the ngrok hook lets through only tools
whose names say they read. Claude's claude.ai connectors belong to the
account rather than this configuration: disconnect them in claude.ai's
settings to keep Claude to the proxy.

# the coding assistant

[ECA](https://eca.dev) is configured here; what follows is only the part that
is particular to this repository. Providers, authentication, agents and the
rest are covered by ECA's own documentation.

## what nix owns

`wgn.home.eca.enable` configures shared skills and model agents here.
The imported `emacs-config` owns the pinned server, sandbox packages and
Emacs launch settings. Enable them with `home.programs.wgn.emacs.eca.enable`
and `home.programs.wgn.emacs.eca.sandbox.enable`.

Nix writes only the files ECA reads and never writes -- `skills/`, `agents/`
and `hooks/` under `~/.config/eca`. `config.json` beside them is yours,
because ECA writes to it itself; a read-only file there is the one thing that
stops it working. Only its MCP proxy entries are merged in on every switch.

Sessions are sandboxed when the sandbox is enabled. `M-x wgn/eca-unsandboxed`
starts one without it, with every skill, including those that drive Emacs;
see `emacs-config`.

## the sandbox image

Home Manager prepares and caches the Gondolin guest image during activation.
The first build requires network access; later activations reuse the image
unless its configuration or the Gondolin version changes. An explicit `--image` selects
a custom image and skips automatic preparation.

See the imported module's [ECA setup](https://github.com/wesnel/emacs-config#eca-and-sandboxed-local-workspaces)
and [guest image provisioning](https://github.com/wesnel/emacs-config#gondolin-guest-image).

Sandbox flags, including Gondolin's `--observe`, `--deny-host` and `--log`,
are documented under [network policy and request logging](https://github.com/wesnel/emacs-config#network-policy-and-request-logging).
The wrappers have different defaults and supported flags; check the
[backend comparison](https://github.com/wesnel/emacs-config#remote-hosts-and-backend-differences).

## reaching a hosted provider

See `emacs-config` for [hosted provider authentication](https://github.com/wesnel/emacs-config#hosted-provider-authentication)
and [network policy](https://github.com/wesnel/emacs-config#network-policy-and-request-logging).
Gondolin's `--share-login` imports an authenticated host session; the flag
is not supported by Bubblewrap.

## running against a model on this machine

With a model served locally, the sandbox can be closed entirely: deny every
host and open one mapping to the server, so reaching a hosted provider is not
something the agent could do rather than something it is asked not to.

``` nix
home.programs.wgn.emacs.eca.sandbox.args = [
  "--http-map" "ollama:11434=127.0.0.1:11434"
  "--http-map" "docs:6280=127.0.0.1:6280"
  "--env" "OLLAMA_API_URL=http://ollama:11434"
];
```

`OLLAMA_API_URL` overrides the `http://localhost:11434` the server would
otherwise use, which inside the guest would be the guest itself.

`wgn.home.ollama.enable` runs the server; the model itself is pulled by hand:

``` sh
ollama pull qwen2.5-coder:7b
```

`wgn.home.eca.localModel` names it on an agent that inherits `code`, so
the model is selected by choosing that agent. It declares nothing else: the
window and cost belong to the provider entry in ECA's `config.json`, and
without them the window is treated as unbounded and a conversation is
truncated rather than compacted.

Set `wgn.home.ollama.contextLength` to the same figure as that entry. Left
unset the server sizes the window from available memory, so it differs
between machines and a client can be promised more room than it will get.

## the documentation index

`wgn.home.docs-mcp-server.enable` runs the index as a service rather than
leaving each client to start its own, so there is one database and one set of
scraped pages. Clients reach it at `http://127.0.0.1:6280/mcp`; the second
mapping above is what carries that into the guest.

Pages are embedded by a model served on this machine, named through the
`openai` provider because that is the API it answers. Pull it before enabling
the service:

``` sh
ollama pull nomic-embed-text
```

`vectorDimension` has to match what that model returns -- 768 for
`nomic-embed-text` -- because only OpenAI's own model sizes are known to the
indexer. Changing either it or `embeddingModel` invalidates the index, since
vectors from one model cannot be compared with another's.

## running the sandbox on a remote host

The devbox is an EC2 guest without hardware virtualization, so its native
Emacs sessions use Bubblewrap. TRAMP sessions started by Emacs on another
machine run the devbox's `eca-sandbox`, and so use Bubblewrap too.

See `emacs-config` for [remote launch behavior and virtualization requirements](https://github.com/wesnel/emacs-config#remote-hosts-and-backend-differences).

## the bubblewrap backend

See the [backend comparison in emacs-config](https://github.com/wesnel/emacs-config#remote-hosts-and-backend-differences)
for filesystem isolation, proxy enforcement and supported flags.

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
