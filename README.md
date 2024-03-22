<!-- markdown-toc start - Don't edit this section. Run M-x markdown-toc-refresh-toc -->
**Table of Contents**

- [directory structure](#directory-structure)
- [updating systems](#updating-systems)
    - [nixOS](#nixos)
    - [nix-darwin](#nix-darwin)
    - [home-manager](#home-manager)
- [troubleshooting](#troubleshooting)
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
├── components
│  └── opinionated reusable configuration modules
├── flake.lock
├── flake.nix <-- main entrypoint
├── LICENSE.txt
└── README.md
```

# updating systems

## nixOS

Assuming you're in this directory:

```bash
sudo nixos-rebuild switch --flake '.#framework'
```

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

## macOS

### `$PATH` gets mangled

On MacOS, there is an `/etc/paths` file and an `/etc/paths.d` directory, which a tool called `path_helper` consults to put things on your `$PATH`. The default `/etc/profile` seems to be responsible for executing `path_helper`. This was never relevant to me or my Mac, until I noticed that everything Nix-related in my `$PATH` was suddenly moved to the end of the `$PATH`. This caused a lot of things to break, since, for example, I would end up using the `git` from `/usr/bin` rather than the one from `/etc/profiles/per-user`. I doubt the following is the "correct" way to fix this, but I seem to have resolved this issue by modifying the `/etc/paths` file using Nix.
