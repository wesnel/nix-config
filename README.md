<!-- markdown-toc start - Don't edit this section. Run M-x markdown-toc-refresh-toc -->
**Table of Contents**

- [directory structure](#directory-structure)
- [adding new systems](#adding-new-systems)
    - [secrets](#secrets)
        - [add the machine's age key to sops config](#add-the-machines-age-key-to-sops-config)
        - [update keys with new host](#update-keys-with-new-host)
    - [building the eca sandbox image](#building-the-eca-sandbox-image)
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

## building the eca sandbox image

`eca-gondolin` runs the ECA server inside a Gondolin micro-VM. The guest lives
in `~/.cache/gondolin`, outside anything Nix manages, so each machine builds it
once:

``` bash
nix shell nixpkgs#e2fsprogs --command \
    gondolin build --config overlays/eca-gondolin/build-config.json \
        --arch aarch64 --tag eca:latest
```

`mke2fs` is the reason for the `nix shell`: Gondolin's wrapper does not carry
e2fsprogs, and the build fails without it. Set `--arch x86_64` to match the
target machine.

The config adds `gcompat` to Alpine's package set. The server is a
glibc-linked native image and Alpine is musl, so without it the binary cannot
start at all.

Point a project at the sandbox from its `.dir-locals.el`:

``` elisp
((nil . ((eca-custom-command . ("eca-sandbox" "--image" "eca:latest"
                                "--allow-host" "api.openai.com")))))
```

`eca-sandbox` is whichever backend the machine provides, so one file serves
every host. `wgn.home.eca.sandbox.backend` selects it: `gondolin` where there
is hardware virtualization, `bubblewrap` otherwise.

Per project rather than globally: `eca-custom-command` is consulted before
`eca' is looked up on a remote host, so a global value would send TRAMP
sessions to this machine's sandbox instead of the host they are editing.

No `eca-local-to-remote-prefix-map` is needed, because the workspace is
mounted in the guest at the path it already has on the host. Translating
instead of mirroring cannot be made to work over TRAMP anyway: the outbound
path conversion strips the TRAMP prefix before applying any mapping, and the
inbound one never restores it, so no single mapping satisfies both directions.

`--allow-host` is default-deny; `models.dev` is always allowed because the
server fetches its model catalog from there on startup.

### running the sandbox on a remote host

`eca-emacs` starts the server with `make-process :file-handler t`, so when
`default-directory` is a TRAMP path the command runs on that host. A
`.dir-locals.el` like the one above, in a project opened over TRAMP, therefore
starts the sandbox on the remote rather than locally.

That host needs `eca-gondolin`, its own guest image built for its
architecture, and **hardware virtualization**. Gondolin runs QEMU, which
without `/dev/kvm` falls back to software emulation and is far too slow to
use. Check before building anything:

``` bash
ls -l /dev/kvm && grep -oE "vmx|svm" /proc/cpuinfo | sort -u
```

Most cloud instances are themselves guests and do not expose this; the devbox
is an EC2 instance with no virtualization extensions at all, so it uses the
`bubblewrap` backend instead.

### the bubblewrap backend

For hosts without virtualization. Bubblewrap gives the filesystem boundary —
`/` read-only, the workspace and state writable — and a local `mitmdump`
enforces `--allow-host`, refusing anything else with a 403 and writing the
same request log as the Gondolin backend.

It is weaker in one specific way, and says so on every start:

```
eca-bwrap: egress is proxy-enforced and bypassable; allowed hosts: models.dev
```

An unprivileged namespace cannot route traffic without a veth pair, so the
choice is the host's network or none at all. The wrapper sets the proxy
variables and forces `no_proxy` empty so a project cannot widen them, and
everything that honours `$HTTPS_PROXY` is covered — the server, and the
`curl` and `git` its tools run. Something that deliberately clears those
variables reaches the network directly. Gondolin has no such gap, because
there the allowlist is enforced by the guest's DNS rather than by consent.

## replacing the devbox

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
deploy '.#devbox'
```

The `devbox` host alias is written into `~/.ssh/config` by the ngrok machine, so
this only resolves where that machine's sops secrets are decrypted.

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
