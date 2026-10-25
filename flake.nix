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

    # x86_64-darwin machines build from these instead: nixpkgs 26.05 is
    # the last release that supports the platform, and 26.11 refuses to
    # evaluate for it. Keep all three on matching 26.05 release branches.
    nixpkgs-x86_64-darwin = {
      url = "github:nixos/nixpkgs/nixpkgs-26.05-darwin";
    };

    nix-darwin-x86_64-darwin = {
      url = "github:lnl7/nix-darwin/nix-darwin-26.05";
      inputs.nixpkgs.follows = "nixpkgs-x86_64-darwin";
    };

    home-manager-x86_64-darwin = {
      url = "github:nix-community/home-manager/release-26.05";
      inputs.nixpkgs.follows = "nixpkgs-x86_64-darwin";
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
    nixpkgs-x86_64-darwin,
    nix-darwin-x86_64-darwin,
    home-manager-x86_64-darwin,
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
    }: darwinModules: homeManagerModules: overlay: let
      isX86 = system == "x86_64-darwin";
      darwin =
        if isX86
        then nix-darwin-x86_64-darwin
        else nix-darwin;
      hm =
        if isX86
        then home-manager-x86_64-darwin
        else home-manager;
    in
      darwin.lib.darwinSystem {
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

            (_: {
              nixpkgs.config.allowDeprecatedx86_64Darwin = isX86;
            })

            # mac-app-util is written in Common Lisp, and nixpkgs marks
            # sbcl broken on x86_64-darwin.
            ({lib, ...}:
              lib.mkIf isX86 {
                services.mac-app-util.enable = false;

                home-manager.sharedModules = [
                  {
                    targets.darwin.mac-app-util.enable = false;
                  }
                ];
              })
          ]
          ++ [
            hm.darwinModules.home-manager
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
      # agent-tools as the overlay builds it, with no host's settings.
      agentTools = pkgs.callPackage ./overlays/agent-tools {};
    in {
      formatter = pkgs.alejandra;

      # agent-tools built, which runs its tests. The devbox deploys it built
      # with its own settings (homeConfigurations.devbox's
      # wgn.home.llm.agentTools), on x86_64-linux, which no machine here
      # can build; .github/workflows/agent-tools-lint.yml builds that one.
      checks.agent-tools = agentTools;

      # emacs-config's plan views read what agent-plan writes: this runs
      # their tests, at the emacs-config this flake locks, against the
      # agent-plan built from this commit. Point it at a checkout with
      # `--override-input emacs-config path:<checkout>`.
      checks.agent-plans =
        pkgs.runCommand "agent-plans-tests" {
          nativeBuildInputs = [
            pkgs.emacs-nox
            pkgs.git
            agentTools
          ];
        } ''
          export HOME=$TMPDIR
          cd ${emacs-config}
          emacs --batch -Q -l tests/run-agent-plans.el \
            --eval '(princ (wgn-agent-plans-run-tests))'
          touch $out
        '';

      # Every playbook template, as agent-plan's built-in templates, starts a
      # plan: `agent-plan new --template` accepts it, its first step is ready,
      # the plan has no structure drift, and each `:AFTER:` names a step of
      # the same template, which agent-plan itself does not check. A failure
      # names the template.
      checks.plan-templates = let
        templates = ./modules/home-manager/llm/skills/playbooks/templates;
      in
        pkgs.runCommand "plan-templates" {
          nativeBuildInputs = [pkgs.gnugrep pkgs.jq agentTools];
        } ''
          export HOME=$TMPDIR AGENT_TOOLS_CONFIG=$TMPDIR/config.json
          jq -n --arg dir "$TMPDIR/notes/agents" --arg templates ${templates} \
            '{plans: {dir: $dir, host: "check", templates: $templates}}' > "$AGENT_TOOLS_CONFIG"
          failed=
          fail() {
            echo "plan-templates: $1: $2" >&2
            failed=1
          }
          for file in ${templates}/*.org; do
            name=$(basename "$file" .org)
            if ! made=$(agent-plan new --title "$name" --slug "$name" --template "$name" 2>&1); then
              fail "$name" "$made"
              continue
            fi
            status=$(agent-plan status "$(jq -r .plan <<<"$made")" --format json)
            first=$(grep -m1 -oP '^:ID: \K\S+' "$file")
            jq -e --arg first "$first" '.readySteps | index($first)' <<<"$status" >/dev/null ||
              fail "$name" "its first step, $first, is not ready"
            drift=$(jq -r '.structureDrift[] | tostring' <<<"$status")
            [ -z "$drift" ] || fail "$name" "structure drift: $drift"
            ids=" $(grep -oP '^:ID: \K\S+' "$file" | tr '\n' ' ') "
            for after in $(grep -oP '^:AFTER:\s*\K.*' "$file"); do
              case "$ids" in
                *" $after "*) ;;
                *) fail "$name" ":AFTER: $after is not a step of it" ;;
              esac
            done
          done
          [ -z "$failed" ]
          touch $out
        '';

      # Every sub-agent type the skills, agent bodies, README and agent-tools'
      # messages name ("`NAME` agent", "`NAME` sub-agent") is one that is
      # defined, and every `pr-loop SUBCOMMAND` they name is in pr-loop's
      # usage, so a rename on one side fails here until the other follows.
      # Defined are the agent files' names (the `name:` line of each in
      # modules/home-manager/claude/agents) and Claude Code's built-in types.
      checks.agent-names = let
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
            ./modules/home-manager/claude/agents
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

      # Every skill the skills, the playbooks skill's plan templates, agent
      # files, AGENTS.md files and README name ("the `NAME` skill", "the `A`
      # and `B` skills", "the =NAME= skill" in a template), and every one an
      # agent file's `skills:` lists, is one the ngrok Mac installs: Claude
      # Code skips a missing preload with only a debug-log warning. Each
      # principle skill, a directory under
      # modules/home-manager/llm/skills/principles, has one home: another
      # skill, an agent file or AGENTS.md names it, and no sentence or clause
      # of eight or more words of it appears in another skill under
      # modules/home-manager/llm or in its AGENTS.md.
      checks.skill-names = let
        ngrok = darwinSystems.ngrok;
        skills = self.darwinConfigurations.ngrok.config.home-manager.users.${ngrok.username}.wgn.home.llm.skills;
        src = pkgs.lib.fileset.toSource {
          root = ./.;
          fileset = pkgs.lib.fileset.unions [
            ./AGENTS.md
            ./README.md
            ./modules/home-manager/llm
            ./modules/home-manager/claude/agents
            ./modules/home-manager/claude/agent-plans
          ];
        };
      in
        pkgs.runCommand "skill-names" {
          nativeBuildInputs = [pkgs.perl];
          # claude/default.nix writes questions and agent-plans itself.
          defined = toString (builtins.attrNames skills ++ ["questions" "agent-plans"]);
          principles = toString (builtins.attrNames (import ./modules/home-manager/llm/principles.nix));
        } ''
          cd ${src}
          perl - <<'PERL'
          use strict;
          use warnings;
          use File::Find;

          my %defined = map { $_ => 1 } split ' ', $ENV{defined};
          my @principles = split ' ', $ENV{principles};
          my $dir = 'modules/home-manager/llm/skills/principles';
          my %text;
          find({no_chdir => 1, wanted => sub {
            return unless /\.md\z/ || m{/skills/playbooks/templates/[^/]+\.org\z};
            open my $fh, '<', $_ or die "$_: $!";
            local $/;
            (my $f = $_) =~ s{^\./}{};
            $text{$f} = <$fh>;
          }}, '.');

          my $n = qr/[A-Za-z][A-Za-z0-9_:-]*/;
          my $sep = qr/,\s+(?:and\s+|or\s+)?|\s+(?:and|or)\s+/;
          my (%named, %listed);
          for my $f (sort keys %text) {
            my $t = $text{$f};
            my $q = $f =~ /\.org\z/ ? '=' : '`';
            while ($t =~ /((?:$q$n$q$sep)*$q$n$q)\s+skills?\b/g) {
              $named{$_}{$f} = 1 for $1 =~ /$q($n)$q/g;
            }
            if ($f =~ m{^modules/home-manager/claude/agents/} && $t =~ /\A---\n(.*?)\n---\n/s) {
              my $front = $1;
              if ($front =~ /^skills:\n((?:  - .*(?:\n|\z))*)/m) {
                $listed{$_}{$f} = 1 for $1 =~ /^  - (\S+)/mg;
              }
            }
          }

          my $failed = 0;
          sub fail { print STDERR @_, "\n"; $failed = 1 }
          sub files { join ' ', sort keys %{$_[0]} }

          for my $name (sort keys %named) {
            fail "no skill $name is defined, but ", files($named{$name}), " name it"
              unless $defined{$name};
          }
          for my $name (sort keys %listed) {
            fail "no skill $name is defined, but the skills: list of ", files($listed{$name}), " names it"
              unless $defined{$name};
          }

          for my $p (@principles) {
            my $own = "$dir/$p/SKILL.md";
            my @by = grep { $_ ne $own } (keys %{$named{$p} // {}}, keys %{$listed{$p} // {}});
            fail "the principle $p is named by no skill, agent file or AGENTS.md" unless @by;
          }

          # Whitespace and bold markers aside; each list item and heading
          # starts a sentence of its own.
          sub body { my $t = shift; $t =~ s/\A---\n.*?\n---\n//s; $t }
          sub flat { my $t = shift; $t =~ s/\*\*//g; $t =~ s/\s+/ /g; $t }
          my %homes = map { $_ => flat(body($text{$_})) }
            grep { m{^modules/home-manager/llm/skills/.*SKILL\.md\z} || $_ eq 'modules/home-manager/llm/AGENTS.md' }
            keys %text;
          for my $p (@principles) {
            my $own = "$dir/$p/SKILL.md";
            my $body = body($text{$own});
            $body =~ s/^#.*$//mg;
            $body =~ s/^\s*(?:[-*]|\d+\.)\s+/\n\n/mg;
            for my $para (split /\n\s*\n/, $body) {
              for my $s (split /(?<=[.!?;:])\s+/, flat($para)) {
                $s =~ s/^\s+|[\s.!?;:]+\z//g;
                next if (() = $s =~ /\S+/g) < 8;
                for my $f (sort keys %homes) {
                  fail "$own and $f both hold: $s"
                    if $f ne $own && index($homes{$f}, $s) >= 0;
                }
              }
            }
          }
          exit $failed;
          PERL
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

      # Every line of a shell code block in the skills and the AGENTS.md
      # files, judged by the guard as a Claude Code Bash call with the ngrok
      # Mac's excluded commands: none is refused for naming an excluded
      # command anywhere but alone, or for running a read of gh's that
      # gh-read runs, so no instruction hands an agent a line its sandbox
      # fails. A <PLACEHOLDER> is read as a word, not a redirection.
      checks.skill-commands = let
        ngrok = darwinSystems.ngrok;
        excluded = self.darwinConfigurations.ngrok.config.home-manager.users.${ngrok.username}.wgn.home.llm.guard.excludedCommands;
        docs = pkgs.lib.fileset.toSource {
          root = ./.;
          fileset = pkgs.lib.fileset.unions [
            ./modules/home-manager/llm/skills
            ./modules/home-manager/llm/AGENTS.md
            ./AGENTS.md
          ];
        };
      in
        pkgs.runCommand "skill-commands" {
          nativeBuildInputs = [pkgs.findutils pkgs.gawk pkgs.jq agentTools];
          config = builtins.toJSON {guard.excludedCommands = excluded;};
          passAsFile = ["config"];
        } ''
          export HOME=$TMPDIR AGENT_TOOLS_CONFIG=$configPath
          cd ${docs}
          # FILE:LINE, a tab, and each command line of an sh, bash, shell,
          # fish, console or unlabelled code block, fenced with ``` or ~~~,
          # continuations joined.
          lines() {
            awk '
              function emit(line, file, at) {
                if (line == "" || line ~ /^#/) return
                gsub(/<[A-Za-z][A-Za-z0-9_ .\/-]*>/, "PLACEHOLDER", line)
                printf "%s:%d\t%s\n", file, at, line
              }
              # A continuation still open where its block ends is a line
              # of its own.
              function flush() {
                if (pending != "") emit(pending, pendfile, start)
                pending = ""
              }
              FNR == 1 { flush(); fence = ""; kind = "" }
              # A fence closes only on a run of its own character at least
              # as long as the one that opened it, with nothing after.
              /^[ \t]*(```|~~~)/ {
                mark = $0; sub(/^[ \t]*/, "", mark)
                ch = substr(mark, 1, 1); run = mark; sub("[^" ch "].*", "", run)
                if (fence != "") {
                  if (substr(fence, 1, 1) == ch && length(run) >= length(fence) && mark ~ ("^" run "[ \t]*$")) {
                    flush(); fence = ""; kind = ""
                  }
                  next
                }
                # Only the first word of the info string counts, as in `bash title=x`.
                fence = run
                info = mark; sub("^" run "[ \t]*", "", info); sub(/[ \t{].*/, "", info)
                kind = (info ~ /^(|sh|bash|shell|fish)$/) ? "shell" : (info == "console") ? "console" : "other"
                next
              }
              # In a console block, the commands are the lines after a $
              # prompt, each joined with the > lines that continue it.
              kind == "console" {
                line = $0; sub(/^[ \t]+/, "", line)
                if (line ~ /^\$ /) {
                  flush(); sub(/^\$ /, "", line)
                  start = FNR; pendfile = FILENAME; pending = line
                } else if (line ~ /^> / && pending != "") {
                  sub(/^> /, "", line); sub(/\\$/, "", pending)
                  pending = pending " " line
                } else {
                  flush()
                }
                next
              }
              kind == "shell" {
                line = $0; sub(/^[ \t]+/, "", line)
                if (pending == "") { start = FNR; pendfile = FILENAME } else { line = pending " " line; pending = "" }
                if (line ~ /\\$/) { sub(/\\$/, "", line); pending = line; next }
                emit(line, FILENAME, start)
              }
              END { flush() }' "$@"
          }
          failed=
          while IFS=$'\t' read -r where line; do
            reason=$(jq -cn --arg c "$line" '{tool_name: "Bash", cwd: "/", tool_input: {command: $c}}' |
              pr-loop guard --baseline | jq -r '.hookSpecificOutput.permissionDecisionReason // empty')
            case "$reason" in
              *"only as a command line of its own"* | '`gh` runs in the sandbox'*)
                printf '%s: %s\n  %s\n' "$where" "$line" "$reason" >&2
                failed=1
                ;;
            esac
          done < <(lines $(find modules/home-manager/llm/skills -name '*.md' | sort) modules/home-manager/llm/AGENTS.md AGENTS.md)
          [ -z "$failed" ]
          touch $out
        '';

      # gosec over agent-tools, with the rules and exclusions in
      # overlays/agent-tools/.golangci.yml; .github/workflows/agent-tools-lint.yml
      # runs it. Run it with `nix build .#checks.<system>.agent-tools-lint`.
      # It is kept out of the package's check phase so that a new gosec rule
      # fails CI and never a switch. It runs without network, on the module
      # dependencies the package vendors (its vendorHash).
      checks.agent-tools-lint =
        pkgs.runCommand "agent-tools-lint" {
          nativeBuildInputs = [pkgs.go pkgs.golangci-lint];
          src = pkgs.lib.fileset.toSource {
            root = ./overlays/agent-tools;
            fileset = pkgs.lib.fileset.unions [
              ./overlays/agent-tools/.golangci.yml
              ./overlays/agent-tools/go.mod
              ./overlays/agent-tools/go.sum
              ./overlays/agent-tools/cmd
              ./overlays/agent-tools/internal
            ];
          };
          vendor = agentTools.goModules;
        } ''
          export HOME=$TMPDIR GOCACHE=$TMPDIR/go-cache GOMODCACHE=$TMPDIR/go-mod \
            GOLANGCI_LINT_CACHE=$TMPDIR/lint-cache GOPROXY=off GOFLAGS=-mod=vendor \
            GOTOOLCHAIN=local CGO_ENABLED=0
          cp -r $src agent-tools
          chmod -R u+w agent-tools
          cp -r $vendor agent-tools/vendor
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
