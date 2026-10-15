{
  description = "Wesley's Nix Configurations";

  inputs = {
    nix-darwin = {
      url = "github:lnl7/nix-darwin/master";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    deploy-rs = {
      url = "github:serokell/deploy-rs";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    emacs-config = {
      url = "github:wesnel/emacs-config";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    emacs-skills = {
      url = "github:xenodium/emacs-skills";
      flake = false;
    };

    flake-utils = {
      url = "github:numtide/flake-utils";
    };

    home-manager = {
      # NOTE: Some options for this (in increasing order of stability)
      #       could be:
      #
      url = "github:nix-community/home-manager";
      #
      # url = "github:nix-community/home-manager/release-25.11";
      #
      inputs.nixpkgs.follows = "nixpkgs";
    };

    mac-app-util = {
      url = "github:hraban/mac-app-util";
    };

    mattpocock-skills = {
      url = "github:mattpocock/skills";
      flake = false;
    };

    nixos-hardware = {
      url = "github:nixos/nixos-hardware/master";
    };

    nixpkgs = {
      # NOTE: Some options for this (in increasing order of stability)
      #       could be:
      #
      # url = "github:nixos/nixpkgs/master";
      #
      url = "github:nixos/nixpkgs/nixos-unstable";
      #
      # url = "github:nixos/nixpkgs/nixos-25.11";
    };

    nur = {
      url = "github:nix-community/NUR";
    };

    sops-nix = {
      url = "github:Mic92/sops-nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = {
    self,
    nix-darwin,
    deploy-rs,
    emacs-config,
    emacs-skills,
    flake-utils,
    home-manager,
    mac-app-util,
    mattpocock-skills,
    nixos-hardware,
    nixpkgs,
    nur,
    sops-nix,
  }: let
    key = "0xA776D2AD099E8BC0";

    sops = {homeDirectory, ...}: {
      sops = {
        defaultSopsFile = ./secrets/wgn.yaml;
        defaultSopsFormat = "yaml";

        age = {
          # This is using an age key that is expected to already be in the filesystem
          keyFile = "${homeDirectory}/.config/sops-nix/key.txt";
          # This will generate a new key if the key specified above does not exist
          generateKey = true;
        };
      };
    };

    homeManagerModules = [
      sops-nix.homeManagerModules.sops
      sops

      mac-app-util.homeManagerModules.default

      (_: {
        _module.args = {
          inherit
            emacs-skills
            mattpocock-skills
            ;
        };
      })

      ./modules/home-manager/aerospace
      ./modules/home-manager/bash
      ./modules/home-manager/claude
      ./modules/home-manager/codex
      ./modules/home-manager/emacs
      ./modules/home-manager/firefox
      ./modules/home-manager/fish
      ./modules/home-manager/fonts
      ./modules/home-manager/foot
      ./modules/home-manager/gamedev
      ./modules/home-manager/games
      ./modules/home-manager/gcloud
      ./modules/home-manager/ghostty
      ./modules/home-manager/git
      ./modules/home-manager/gnupg
      ./modules/home-manager/go
      ./modules/home-manager/helix
      ./modules/home-manager/hyprland
      ./modules/home-manager/iterm
      ./modules/home-manager/llm
      ./modules/home-manager/man
      ./modules/home-manager/mcp-proxy
      ./modules/home-manager/mosh
      ./modules/home-manager/music
      ./modules/home-manager/photos
      ./modules/home-manager/python
      ./modules/home-manager/slack
      ./modules/home-manager/sway
      ./modules/home-manager/tex
      ./modules/home-manager/video
      ./modules/home-manager/virtualisation
      ./modules/home-manager/yubikey
      ./modules/home-manager/zellij
      ./modules/home-manager/zoom
      ./modules/home-manager/zwift
    ];

    nixosModules = [
      sops-nix.nixosModules.sops
      sops

      ./modules/nixos/emacs
      ./modules/nixos/fish
      ./modules/nixos/fonts
      ./modules/nixos/hyprland
      ./modules/nixos/interception-tools
      ./modules/nixos/kde
      ./modules/nixos/mullvad
      ./modules/nixos/networking
      ./modules/nixos/nix
      ./modules/nixos/sddm
      ./modules/nixos/steam
      ./modules/nixos/users
      ./modules/nixos/virtualisation
      ./modules/nixos/wayland
      ./modules/nixos/yubikey
      ./modules/nixos/zsa
      ./modules/nixos/zwift
    ];

    darwinModules = [
      mac-app-util.darwinModules.default

      ./modules/darwin/defaults
      ./modules/darwin/emacs
      ./modules/darwin/fish
      ./modules/darwin/fonts
      ./modules/darwin/homebrew
      ./modules/darwin/networking
      ./modules/darwin/nix
      ./modules/darwin/paths
      ./modules/darwin/users
      ./modules/darwin/yubikey
    ];

    buildNixosConfiguration = args @ {
      computerName,
      username,
      homeDirectory,
      key,
      system,
      extraNixOSModules,
      extraHomeManagerModules,
    }: nixosModules: homeManagerModules: overlay:
      nixpkgs.lib.nixosSystem {
        inherit
          system
          ;

        modules =
          nixosModules
          ++ extraNixOSModules
          ++ [
            ({lib, ...}: {
              system.stateVersion = lib.mkDefault "22.05";
            })

            (_: {
              nixpkgs.overlays = [
                nur.overlays.default
                emacs-config.overlays.default
                overlay
              ];
            })

            (_: {
              nixpkgs.config.allowUnfree = true;
            })

            (_: {
              wgn.nixos = {
                zsa.enable = true;
              };
            })
          ]
          ++ [
            home-manager.nixosModules.home-manager
            {
              home-manager = {
                users = {
                  "${username}" = {lib, ...}: {
                    home.stateVersion = lib.mkDefault "22.05";
                    programs.home-manager.enable = true;
                    imports = homeManagerModules ++ extraHomeManagerModules;
                  };
                };

                backupFileExtension = "backup";
                extraSpecialArgs = args;
                useUserPackages = true;
                useGlobalPkgs = true;
                verbose = true;
              };
            }
          ];

        specialArgs = args;
      };

    nixosSystems = import ./machines/nixos {
      inherit
        emacs-config
        nixos-hardware
        ;
    };

    buildDarwinConfiguration = args @ {
      computerName,
      username,
      homeDirectory,
      key,
      system,
      extraHomeManagerModules,
      extraDarwinModules,
    }: darwinModules: homeManagerModules: overlay:
      nix-darwin.lib.darwinSystem {
        inherit
          system
          ;

        modules =
          darwinModules
          ++ extraDarwinModules
          ++ [
            (_: {
              system.configurationRevision = self.rev or self.dirtyRev or null;
              system.stateVersion = 6;
            })

            (_: {
              nixpkgs.overlays = [
                nur.overlays.default
                emacs-config.overlays.default
                overlay
              ];
            })
          ]
          ++ [
            home-manager.darwinModules.home-manager
            {
              home-manager = {
                users = {
                  "${username}" = {lib, ...}: {
                    home.stateVersion = lib.mkDefault "22.05";
                    programs.home-manager.enable = true;
                    imports = homeManagerModules ++ extraHomeManagerModules;
                  };
                };

                extraSpecialArgs = args;
                useUserPackages = true;
                useGlobalPkgs = true;
                verbose = true;
              };
            }
          ];

        specialArgs = args;
      };

    darwinSystems = import ./machines/darwin {
      inherit
        emacs-config
        ;
    };

    buildHomeManagerConfiguration = args @ {
      computerName,
      username,
      homeDirectory,
      key,
      system,
      extraHomeManagerModules,
    }: homeManagerModules: overlay:
      home-manager.lib.homeManagerConfiguration {
        pkgs = import nixpkgs {
          inherit system;

          overlays = [
            nur.overlays.default
            emacs-config.overlays.default
            overlay
          ];

          config = {
            allowUnfree = true;
          };
        };

        modules =
          homeManagerModules
          ++ extraHomeManagerModules
          ++ [
            ({lib, ...}: {
              home = {
                inherit
                  username
                  homeDirectory
                  ;

                stateVersion = lib.mkDefault "22.05";
              };

              programs.home-manager.enable = true;
            })
          ];

        extraSpecialArgs = args;
      };

    homeManagerSystems = import ./machines/home-manager {
      inherit
        emacs-config
        ;
    };

    # deploy-rs.lib for a given system, with the deploy-rs binary that the
    # activation wrappers embed taken from nixpkgs rather than built from the
    # flake's own source.
    deployLib = system: let
      pkgs = import nixpkgs {
        inherit system;
      };
    in
      (import nixpkgs {
        inherit system;

        overlays = [
          deploy-rs.overlays.default

          (_: prev: {
            deploy-rs = {
              inherit (prev.deploy-rs) lib;
              inherit (pkgs) deploy-rs;
            };
          })
        ];
      })
      .deploy-rs
      .lib;

    buildDeployNode = {
      computerName,
      username,
      homeDirectory,
      system,
      deploy,
      extraHomeManagerModules,
    }: homeConfiguration:
      deploy
      // {
        profiles = {
          home = {
            user = username;
            sshUser = username;

            # home-manager points ../profiles/home-manager at its own
            # generation on every activation, so deploy-rs needs a profile of
            # its own to roll back.
            profilePath = "${homeDirectory}/.local/state/nix/profiles/home";

            path = (deployLib system).activate.home-manager homeConfiguration;
          };
        };
      };
  in
    flake-utils.lib.eachDefaultSystemPassThrough (system: rec {
      overlays = {
        default = import ./overlays;
      };

      darwinConfigurations = let
        op = _: {
          computerName,
          username,
          homeDirectory,
          system,
          extraHomeManagerModules,
          extraDarwinModules,
        }:
          buildDarwinConfiguration
          {
            inherit
              computerName
              username
              homeDirectory
              key
              system
              extraHomeManagerModules
              extraDarwinModules
              ;
          }
          darwinModules
          homeManagerModules
          overlays.default;
      in (builtins.mapAttrs op darwinSystems);

      nixosConfigurations = let
        op = _: {
          computerName,
          username,
          homeDirectory,
          system,
          extraNixOSModules,
          extraHomeManagerModules,
        }:
          buildNixosConfiguration
          {
            inherit
              computerName
              username
              homeDirectory
              key
              system
              extraNixOSModules
              extraHomeManagerModules
              ;
          }
          nixosModules
          homeManagerModules
          overlays.default;
      in (builtins.mapAttrs op nixosSystems);

      deploy = {
        nodes = let
          op = name: {
            computerName,
            username,
            homeDirectory,
            system,
            deploy,
            extraHomeManagerModules,
          }:
            buildDeployNode
            {
              inherit
                computerName
                username
                homeDirectory
                system
                deploy
                extraHomeManagerModules
                ;
            }
            homeConfigurations.${name};
        in (builtins.mapAttrs op homeManagerSystems);
      };

      homeConfigurations = let
        op = _: {
          computerName,
          username,
          homeDirectory,
          system,
          deploy,
          extraHomeManagerModules,
        }:
          buildHomeManagerConfiguration
          {
            inherit
              computerName
              username
              homeDirectory
              key
              system
              extraHomeManagerModules
              ;
          }
          homeManagerModules
          overlays.default;
      in (builtins.mapAttrs op homeManagerSystems);
    })
    // flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs {
        inherit system;
      };
    in {
      formatter = pkgs.alejandra;

      # emacs-config's plan views read what agent-plan writes: this runs
      # their tests, at the emacs-config this flake locks, against the
      # agent-plan built from this commit. Point it at a checkout with
      # `--override-input emacs-config path:<checkout>`.
      checks.agent-plans =
        pkgs.runCommand "agent-plans-tests" {
          nativeBuildInputs = [
            pkgs.emacs-nox
            pkgs.git
            (pkgs.callPackage ./overlays/agent-tools {})
          ];
        } ''
          export HOME=$TMPDIR
          cd ${emacs-config}
          emacs --batch -Q -l tests/run-agent-plans.el \
            --eval '(princ (wgn-agent-plans-run-tests))'
          touch $out
        '';

      # Every sub-agent type the skills, agent bodies, README and agent-tools'
      # messages name ("`NAME` agent", "`NAME` sub-agent") is one that is
      # defined, and every `pr-loop SUBCOMMAND` they name is in pr-loop's
      # usage, so a rename on one side fails here until the other follows.
      # Defined are the agent files' names (the `name:` line of each in
      # modules/home-manager/claude/agents) and Claude Code's built-in types.
      checks.agent-names = let
        agentTools = pkgs.callPackage ./overlays/agent-tools {};
        agentFiles = pkgs.lib.filterAttrs (name: kind: kind == "regular" && pkgs.lib.hasSuffix ".md" name && name != "subagent.md")
          (builtins.readDir ./modules/home-manager/claude/agents);
        fileNames = pkgs.lib.mapAttrsToList (file: _: let
          lines = pkgs.lib.splitString "\n" (builtins.readFile (./modules/home-manager/claude/agents + "/${file}"));
          named = builtins.filter (pkgs.lib.hasPrefix "name: ") lines;
        in
          assert pkgs.lib.assertMsg (named != []) "agent-names: ${file} has no name: line";
            pkgs.lib.removePrefix "name: " (builtins.head named))
        agentFiles;
        defined = fileNames ++ ["Explore" "general-purpose" "Plan"];
        src = pkgs.lib.fileset.toSource {
          root = ./.;
          fileset = pkgs.lib.fileset.unions [
            ./README.md
            ./modules/home-manager/llm
            ./overlays/agent-tools/cmd
            ./overlays/agent-tools/internal
          ];
        };
      in
        pkgs.runCommand "agent-names" {nativeBuildInputs = [pkgs.gnugrep agentTools];} ''
          cd ${src}
          # In Markdown a code block's line names a command too; in Go only
          # a backticked one does.
          files() {
            { grep -rlP "$1" --include='*.md' .; grep -rlP "$2" --include='*.go' --exclude='*_test.go' .; } | tr '\n' ' '
          }
          names() {
            { grep -rhoP "$1" --include='*.md' .; grep -rhoP "$2" --include='*.go' --exclude='*_test.go' .; } | sort -u
          }
          failed=
          defined=" ${pkgs.lib.concatStringsSep " " defined} "
          agent='`\K[A-Za-z][A-Za-z0-9_:-]*(?=` (sub-?)?agents?\b)'
          for name in $(names "$agent" "$agent"); do
            case "$defined" in
              *" ''${name##*:} "*) ;;
              *)
                named="\`$name\` (sub-?)?agents?\b"
                echo "no sub-agent type $name is defined, but $(files "$named" "$named")name it" >&2
                failed=1
                ;;
            esac
          done
          subcommands=" $(pr-loop -h 2>&1 | grep -oP '^  \K[a-z][a-z-]*(?= |$)' | sort -u | tr '\n' ' ') "
          for sub in $(names '(?:^\s*|`)pr-loop \K[a-z][a-z-]*' '`pr-loop \K[a-z][a-z-]*'); do
            case "$subcommands" in
              *" $sub "*) ;;
              *)
                echo "pr-loop has no subcommand $sub, but $(files "(?:^\s*|\`)pr-loop $sub(?![\w-])" "\`pr-loop $sub(?![\w-])")name it" >&2
                failed=1
                ;;
            esac
          done
          [ -z "$failed" ]
          touch $out
        '';

      # The Claude Code settings the ngrok Mac's home configuration writes
      # hold the permission rules beside the guard: bypass mode off; for
      # every server mcp.json lists, each read allowed and each confirm
      # asked, never allowed; no allow rule that a deny rule covers, which
      # deny would override; and the guard's PreToolUse hook matching MCP
      # tools, every hook of it blocking when it cannot run.
      checks.claude-settings = let
        ngrok = darwinSystems.ngrok;
        settings = self.darwinConfigurations.ngrok.config.home-manager.users.${ngrok.username}.wgn.home.claude.settings;
      in
        pkgs.runCommand "claude-settings" {
          nativeBuildInputs = [pkgs.jq];
          settings = builtins.toJSON settings;
          mcp = builtins.readFile ./overlays/agent-tools/internal/guard/mcp.json;
          passAsFile = ["settings" "mcp"];
        } ''
          s=$settingsPath
          # Each check prints what breaks it, and fails on any.
          check() {
            found=$(jq -r "$@" "$s")
            if [ -n "$found" ]; then
              printf '%s\n' "$found" >&2
              failed=1
            fi
          }
          failed=
          check 'if .permissions.disableBypassPermissionsMode == "disable" then empty
            else "bypass-permissions mode is not disabled" end'
          check '.permissions.deny as $d | ["Bash(gh pr merge *)", "Bash(git push *)"][]
            | select(. as $t | $d | index([$t]) == null) | "not denied: \(.)"'
          check --slurpfile mcp "$mcpPath" '. as $s | $mcp[0][] | .servers[] as $server
            | ((.reads // [])[] | "mcp__\($server)__\(.)"
                | select(. as $t | $s.permissions.allow | index([$t]) == null) | "read not allowed: \(.)"),
              ((.confirms // [])[] | "mcp__\($server)__\(.)"
                | (select(. as $t | $s.permissions.ask | index([$t]) == null) | "confirm not asked: \(.)"),
                  (select(. as $t | $s.permissions.allow | index([$t]) != null) | "confirm allowed: \(.)"))'
          # A deny rule covers an allow rule its pattern matches, with `*`
          # any text; one with no parentheses covers its whole tool, or
          # every tool of its MCP server.
          check 'def rx: gsub("(?<c>[.+?^$|{}()\\[\\]\\\\])"; "\\\(.c)") | gsub("\\*"; ".*");
            def covers($d): if ($d | test("\\(")) then test("^" + ($d | rx) + "$")
              else . == $d or startswith($d + "(") or startswith($d + "__") end;
            .permissions.deny as $deny | .permissions.allow[] as $a | $deny[]
            | select(. as $d | $a | covers($d)) | "allowed but denied: \($a), by \(.)"'
          check 'if (.hooks.PreToolUse | any(.matcher | test("mcp__"))) then empty
            else "no PreToolUse hook matches MCP tools" end'
          check '.hooks.PreToolUse[].hooks[] | select(.onFailure != "block") | "a PreToolUse hook does not block on failure: \(.command)"'
          [ -z "$failed" ]
          touch $out
        '';

      # gosec over agent-tools, with the rules and exclusions in
      # overlays/agent-tools/.golangci.yml; .github/workflows/agent-tools-lint.yml
      # runs it. Run it with `nix build .#checks.<system>.agent-tools-lint`.
      # It is kept out of the package's check phase so that a new gosec rule
      # fails CI and never a switch. It runs without network, which holds
      # while agent-tools has no module dependencies.
      checks.agent-tools-lint =
        pkgs.runCommand "agent-tools-lint" {
          nativeBuildInputs = [pkgs.go pkgs.golangci-lint];
          src = pkgs.lib.fileset.toSource {
            root = ./overlays/agent-tools;
            fileset = pkgs.lib.fileset.unions [
              ./overlays/agent-tools/.golangci.yml
              ./overlays/agent-tools/go.mod
              ./overlays/agent-tools/cmd
              ./overlays/agent-tools/internal
            ];
          };
        } ''
          export HOME=$TMPDIR GOCACHE=$TMPDIR/go-cache GOMODCACHE=$TMPDIR/go-mod \
            GOLANGCI_LINT_CACHE=$TMPDIR/lint-cache GOPROXY=off GOFLAGS=-mod=readonly \
            GOTOOLCHAIN=local CGO_ENABLED=0
          cp -r $src agent-tools
          chmod -R u+w agent-tools
          cd agent-tools
          golangci-lint run ./...
          touch $out
        '';

      devShells = {
        default = pkgs.mkShell {
          buildInputs = [
            # Not `with pkgs`: the deploy-rs flake input shadows pkgs.deploy-rs.
            pkgs.alejandra
            pkgs.deploy-rs
            pkgs.nil
          ];
        };
      };
    });
}
