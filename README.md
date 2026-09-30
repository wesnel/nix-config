<!-- markdown-toc start - Don't edit this section. Run M-x markdown-toc-refresh-toc -->
**Table of Contents**

- [directory structure](#directory-structure)
- [adding new systems](#adding-new-systems)
    - [secrets](#secrets)
        - [add the machine's age key to sops config](#add-the-machines-age-key-to-sops-config)
        - [update keys with new host](#update-keys-with-new-host)
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
stops it working.

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
scraped pages. Clients find it at `http://127.0.0.1:6280/mcp`, which
`programs.mcp` writes for them; the second mapping above is what carries that
into the guest.

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
