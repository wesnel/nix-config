<!-- markdown-toc start - Don't edit this section. Run M-x markdown-toc-refresh-toc -->
**Table of Contents**

- [directory structure](#directory-structure)
- [updating systems](#updating-systems)
    - [nixOS](#nixos)
    - [nix-darwin](#nix-darwin)
    - [home-manager](#home-manager)
- [troubleshooting](#troubleshooting)
    - [emacs](#emacs)
        - [config change not applying](#config-change-not-applying)
        - [weird environment variables](#weird-environment-variables)
    - [macOS](#macos)
        - [`$PATH` gets mangled](#path-gets-mangled)

<!-- markdown-toc end -->
# directory structure

```
.
├── machines
│  ├── nixOS configurations
│  └── nix-darwin configurations
├── modules
│  ├── programs
│  │  └── nixOS `programs` modules
│  ├── services
│  │  └── nixOS `services` modules
│  ├── home
│  │  ├── programs
│  │  │  └── home-manager `programs` modules
│  │  ├── services
│  │  │  └── home-manager `services` modules
│  │  ├── users
│  │  │  └── home-manager user configs
│  │  └── other home-manager modules
│  └── other nixOS or nix-darwin modules
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
darwin-rebuild switch --flake '.#wgn-shipt'
```

## home-manager

Assuming you're in this directory:

```bash
home-manager switch --flake '.#wgn-shipt'
```

# troubleshooting

## emacs

### config change not applying

``` bash
doom sync
```

### weird environment variables

```bash
doom env -a '^SSH_'
```

## macOS

### `$PATH` gets mangled

On MacOS, there is an `/etc/paths` file and an `/etc/paths.d` directory, which a tool called `path_helper` consults to put things on your `$PATH`. The default `/etc/profile` seems to be responsible for executing `path_helper`. This was never relevant to me or my Mac, until I noticed that everything Nix-related in my `$PATH` was suddenly moved to the end of the `$PATH`. This caused a lot of things to break, since, for example, I would end up using the `git` from `/usr/bin` rather than the one from `/etc/profiles/per-user`. I doubt the following is the "correct" way to fix this, but I seem to have resolved this issue by modifying the `/etc/paths` file using Nix, as you can see in [my config](./modules/etc/paths/default.nix).

### `tput: unknown terminal "xterm-kitty"`

I need to find a better solution to this.

``` bash
TERM='xterm' darwin-rebuild switch --flake '.#wgn-shipt'
```
