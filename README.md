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
