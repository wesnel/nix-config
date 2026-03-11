<!-- markdown-toc start - Don't edit this section. Run M-x markdown-toc-refresh-toc -->
**Table of Contents**

- [directory structure](#directory-structure)
- [updating systems](#updating-systems)
    - [nixOS](#nixos)
    - [orbstack nixOS virtual machine](#orbstack-nixos-virtual-machine)
    - [nix-darwin](#nix-darwin)
    - [home-manager](#home-manager)
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

### generate host key

Generate an SSH key if one does not already exist:

``` bash
sudo ssh-keygen -t rsa -b 4096 -f /etc/ssh/ssh_host_rsa_key -N ""
```

Next, import it into GPG:

``` bash
nix-shell -p gnupg -p ssh-to-pgp --run "sudo ssh-to-pgp -private-key -i /etc/ssh/ssh_host_rsa_key | gpg --import --quiet"
```

This will import the key and also print the key fingerprint to `stdout`.

### add public key to git repo

Let's assume that the fingerprint from the last step is `c56666da854b90c1c4fa3de2089ea4f8f38b1960`. Run the following in the root of the repo:

``` bash
FINGERPRINT=c56666da854b90c1c4fa3de2089ea4f8f38b1960 gpg --export $FINGERPRINT > keys/hosts/$FINGERPRINT.asc
```

### add host key to sops config

Add the key fingerprint to `.sops.yaml` by following the pattern set in that file for other machines.

### update keys with new host

``` bash
nix-shell --run "sops updatekeys secrets/wgn.yaml"
```

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
darwin-rebuild switch --flake '.#shipt'
```

## home-manager

Assuming you're in this directory:

```bash
home-manager switch --flake '.#shipt'
```

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
