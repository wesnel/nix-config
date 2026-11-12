{
  description = "Wesley's Nix Configurations";

  inputs = {
    nix-darwin = {
      url = "github:lnl7/nix-darwin/master";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    cursor-plugins = {
      url = "github:cursor/plugins";
      flake = false;
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
      inputs.nixpkgs.follows = "nixpkgs";
    };

    mattpocock-skills = {
      url = "github:mattpocock/skills";
      flake = false;
    };

    nix-flatpak = {
      # A release tag: its main branch is unstable. The tag is named in
      # full, since a branch of the same name would win over it.
      url = "github:gmodena/nix-flatpak?ref=refs/tags/v0.8.0";
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

    sops-nix = {
      url = "github:Mic92/sops-nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = {
    self,
    nix-darwin,
    cursor-plugins,
    deploy-rs,
    emacs-config,
    emacs-skills,
    flake-utils,
    home-manager,
    mac-app-util,
    mattpocock-skills,
    nix-flatpak,
    nixos-hardware,
    nixpkgs,
    nixpkgs-x86_64-darwin,
    nix-darwin-x86_64-darwin,
    home-manager-x86_64-darwin,
    sops-nix,
  }: let
    # The full fingerprint: pr-loop open and git sign-pass prove signatures
    # against it, which a key ID is too short to do.
    key = "F84480B20CA9D6CCC7F52479A776D2AD099E8BC0";

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

      # Inert unless the system enables Flatpak.
      nix-flatpak.homeManagerModules.nix-flatpak

      (_: {
        _module.args = {
          inherit
            cursor-plugins
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
      ./modules/home-manager/otel-collector
      ./modules/home-manager/photos
      ./modules/home-manager/python
      ./modules/home-manager/slack
      ./modules/home-manager/sway
      ./modules/home-manager/tex
      ./modules/home-manager/video
      ./modules/home-manager/virtualisation
      ./modules/home-manager/yubikey
      ./modules/home-manager/zellij
      ./modules/home-manager/zen
      ./modules/home-manager/zoom
      ./modules/home-manager/zwift
    ];

    nixosModules = [
      sops-nix.nixosModules.sops
      sops

      ./modules/nixos/emacs
      ./modules/nixos/fish
      ./modules/nixos/flatpak
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
      ./modules/darwin/zen
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
      # The name of a check of aarch64-darwin alone, and null, which Nix
      # leaves out as an attribute's name, on every other system: a check
      # that reads a darwin configuration builds that configuration's
      # derivations, which only an aarch64-darwin builder can build.
      darwinOnly = name:
        if system == "aarch64-darwin"
        then name
        else null;
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
      # `--override-input emacs-config path:<checkout>`. The agent-plan is
      # built without a baked config home, so it reads the scratch config
      # the tests name in AGENT_TOOLS_CONFIG. Emacs has org-roam, which the
      # tests of plans as org-roam nodes skip without, and fails at once
      # where it cannot load it.
      checks.agent-plans = let
        emacs = pkgs.emacs-nox.pkgs.withPackages (epkgs: [epkgs.org-roam]);
      in
        pkgs.runCommand "agent-plans-tests" {
          nativeBuildInputs = [
            emacs
            pkgs.git
            agentTools
          ];
        } ''
          export HOME=$TMPDIR
          cd ${emacs-config}
          emacs --batch -Q --eval '(require (quote org-roam))' -l tests/run-agent-plans.el \
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
            first=$(grep -m1 -oP '^:CUSTOM_ID: \K\S+' "$file")
            jq -e --arg first "$first" '.readySteps | index($first)' <<<"$status" >/dev/null ||
              fail "$name" "its first step, $first, is not ready"
            drift=$(jq -r '.structureDrift[] | tostring' <<<"$status")
            [ -z "$drift" ] || fail "$name" "structure drift: $drift"
            ids=" $(grep -oP '^:CUSTOM_ID: \K\S+' "$file" | tr '\n' ' ') "
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
        agentFiles =
          pkgs.lib.filterAttrs (name: kind: kind == "regular" && pkgs.lib.hasSuffix ".md" name && name != "subagent.md")
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
      # files, AGENTS.md files, CHECKS.md and README name ("the `NAME`
      # skill", "the `A` and `B` skills", "the =NAME= skill" in a
      # template), and every one an
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
            ./CHECKS.md
            ./README.md
            ./modules/home-manager/llm
            ./modules/home-manager/claude/agents
          ];
        };
      in
        pkgs.runCommand "skill-names" {
          nativeBuildInputs = [pkgs.perl];
          defined = toString (builtins.attrNames skills);
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

      checks.${darwinOnly "codex-rules"} = let
        ngrok = self.darwinConfigurations.ngrok.config.home-manager.users.${darwinSystems.ngrok.username};
      in
        pkgs.runCommand "codex-rules" {
          nativeBuildInputs = [pkgs.python3 pkgs.bash];
          activation = ngrok.home.activation.codexRules.data;
          opsActivation = ngrok.home.activation.codexOpsServer.data;
          statusLineActivation = ngrok.home.activation.codexStatusLine.data;
          otelActivation = ngrok.home.activation.codexOtel.data;
          otelUrl = ngrok.wgn.home.otel-collector.url;
          passAsFile = ["activation" "opsActivation" "statusLineActivation" "otelActivation"];
        } ''
          python3 ${./modules/home-manager/codex/rules-tests.py} "$activationPath"
          python3 ${./modules/home-manager/codex/ops-tests.py} "$opsActivationPath"
          python3 ${./modules/home-manager/codex/statusline-tests.py} "$statusLineActivationPath"
          python3 ${./modules/home-manager/codex/otel-tests.py} "$otelActivationPath" "$otelUrl"
          touch $out
        '';

      checks.${darwinOnly "plan-mirrors"} = let
        ngrok = self.darwinConfigurations.ngrok.config.home-manager.users.${darwinSystems.ngrok.username};
        devbox = self.homeConfigurations.devbox.config;
        valid = import ./modules/home-manager/llm/plan-mirrors-check.nix {inherit (pkgs) lib;};
        actual = {
          enabled = ngrok.wgn.home.llm.agentPlans.enable;
          watcher = ngrok.launchd.agents.plan-mirrors or null;
          plansDir = ngrok.wgn.home.llm.plans.dir;
          devboxEnabled = devbox.wgn.home.llm.agentPlans.enable;
          devboxService = devbox.systemd.user.services ? agent-plans;
          claudeHooks = ngrok.wgn.home.claude.settings.hooks;
          codexHooks = (builtins.fromJSON ngrok.home.file.".codex/hooks.json".text).hooks;
          sharedSkill = ngrok.home.file ? ".claude/skills/plan-mirrors" && ngrok.home.file ? ".codex/skills/plan-mirrors";
          claudeExclusions = ngrok.wgn.home.claude.settings.sandbox.excludedCommands;
          claudeAllow = ngrok.wgn.home.claude.settings.permissions.allow;
          claudeWrites = ngrok.wgn.home.claude.settings.sandbox.filesystem.allowWrite;
          codexFilesystem = ngrok.wgn.home.codex.sandbox.permissions.wgn.filesystem;
          home = ngrok.home.homeDirectory;
          mirrorState = "${ngrok.xdg.stateHome}/agent-plans/mirrors";
        };
        broken = field: value:
          actual
          // {
            watcher =
              actual.watcher
              // {
                config = actual.watcher.config // {${field} = value;};
              };
          };
        brokenHook = harness: event: suffix: matcher:
          actual
          // {
            ${harness} =
              actual.${harness}
              // {
                ${event} =
                  map
                  (group:
                    if builtins.any (hook: pkgs.lib.hasSuffix suffix (hook.command or "")) (group.hooks or [])
                    then group // {inherit matcher;}
                    else group)
                  actual.${harness}.${event};
              };
          };
      in
        assert pkgs.lib.assertMsg (valid actual) "Plan mirrors need the Mac refresh watcher, both harness hooks and skills, and no devbox dashboard service";
        assert pkgs.lib.assertMsg (builtins.all
          (harness:
            !(valid (brokenHook harness "PreToolUse" "pr-loop guard --baseline" "Bash|mcp__.*"))
            && !(valid (brokenHook harness "PostToolUse" "plan-dash record" "Artifact"))
            && !(valid (brokenHook harness "PostToolUse" "plan-dash record" "mcp__.*"))
            && !(valid (brokenHook harness "PostToolUse" "plan-dash record" ".*")))
          ["claudeHooks" "codexHooks"])
        "Native publications need baseline guard coverage and narrowly matched receipt hooks";
        assert pkgs.lib.assertMsg (!(valid (broken "WatchPaths" []))) "Plan mirror check must reject a missing path trigger";
        assert pkgs.lib.assertMsg (!(valid (broken "StartInterval" 0))) "Plan mirror check must reject a missing timer";
        assert pkgs.lib.assertMsg (!(valid (broken "EnvironmentVariables" {PATH = "/usr/bin:/bin";}))) "Plan mirror check must reject a PATH that cannot find the installed Codex";
        assert pkgs.lib.assertMsg (!(valid (actual // {claudeExclusions = [];}))) "Plan mirror commands need narrow sandbox exclusions";
        assert pkgs.lib.assertMsg (!(valid (actual // {claudeExclusions = actual.claudeExclusions ++ ["plan-dash *"];}))) "Plan mirror exclusions must not cover every subcommand";
        assert pkgs.lib.assertMsg (!(valid (actual // {claudeAllow = [];}))) "Plan mirror commands need narrow native allow rules";
        assert pkgs.lib.assertMsg (!(valid (actual // {claudeAllow = actual.claudeAllow ++ ["Bash(plan-dash *)"];}))) "Plan mirror native rules must not cover every subcommand";
        assert pkgs.lib.assertMsg (!(valid (actual // {claudeWrites = actual.claudeWrites ++ [actual.mirrorState];}))) "Claude must keep mirror state outside writable roots";
        assert pkgs.lib.assertMsg (!(valid (actual // {codexFilesystem = actual.codexFilesystem // {${actual.mirrorState} = "write";};}))) "Codex must keep mirror state outside writable roots";
          pkgs.runCommand "plan-mirrors" {} ''
            touch $out
          '';

      checks.${darwinOnly "agent-plan-sync"} = let
        ngrok = self.darwinConfigurations.ngrok.config.home-manager.users.${darwinSystems.ngrok.username};
        devbox = self.homeConfigurations.devbox.config;
        activation = ngrok.home.activation.agentPlans;
        darwinWatcher = ngrok.launchd.agents.agent-plan-sync;
        linuxPath = devbox.systemd.user.paths.agent-plan-sync;
        linuxTimer = devbox.systemd.user.timers.agent-plan-sync;
        linuxExec = devbox.systemd.user.services.agent-plan-sync.Service.ExecStart;
        fixtureAgent = pkgs.writeShellScript "agent-plan-fixture" ''
          printf '%s\n' "$*" >>"$PLAN_TEST_LOG"
          if [[ "$1" == init ]]; then exit "''${PLAN_INIT_EXIT:-0}"; fi
        '';
      in
        assert pkgs.lib.assertMsg (darwinWatcher.enable && (darwinWatcher.config.WatchPaths or []) == [ngrok.wgn.home.llm.plans.dir] && (darwinWatcher.config.StartInterval or null) == 300)
        "Darwin plan sync needs both its path watcher and periodic trigger";
        assert pkgs.lib.assertMsg (builtins.tail darwinWatcher.config.ProgramArguments == ["--settle" "5s"] && pkgs.lib.hasSuffix "-agent-plan-sync" (builtins.head darwinWatcher.config.ProgramArguments) && builtins.length linuxExec == 1 && pkgs.lib.hasSuffix "-agent-plan-sync --settle 5s" (builtins.head linuxExec))
        "Both plan watchers must call the initialization-and-sync runner";
        assert pkgs.lib.assertMsg ((linuxPath.Path.PathChanged or null) == devbox.wgn.home.llm.plans.dir && builtins.elem "default.target" (linuxPath.Install.WantedBy or []) && (linuxTimer.Timer.OnActiveSec or null) == "5min" && (linuxTimer.Timer.OnUnitActiveSec or null) == "5min" && builtins.elem "timers.target" (linuxTimer.Install.WantedBy or []))
        "Linux plan sync needs both its enabled path watcher and periodic timer";
          pkgs.runCommand "agent-plan-sync" {
            nativeBuildInputs = [pkgs.coreutils pkgs.gnused pkgs.gnugrep];
            activation = activation.data;
            agentPlan = pkgs.lib.getExe' ngrok.wgn.home.llm.agentTools "agent-plan";
            notesKey = ngrok.wgn.home.llm.plans.repository.key;
            runner = builtins.head darwinWatcher.config.ProgramArguments;
            inherit fixtureAgent;
            passAsFile = ["activation"];
          } ''
            export runnerPath="$runner"
            bash ${./modules/home-manager/llm/plan-activation-tests.sh}
            touch $out
          '';

      checks.${darwinOnly "parallels-mcp"} = let
        hm = self.darwinConfigurations.ngrok.config.home-manager.users.wgn;
        package = self.darwinConfigurations.ngrok.pkgs.pdfm-mcp;
      in
        pkgs.runCommand "parallels-mcp" {
          nativeBuildInputs = [pkgs.python3 pkgs.bash];
          binary = pkgs.lib.getExe package;
          inventory = ./overlays/agent-tools/internal/guard/mcp.json;
          serve = builtins.head hm.launchd.agents.mcp-proxy.config.ProgramArguments;
          codexActivation = pkgs.writeText "codex-mcp-activation" hm.home.activation.codexMcpServers.data;
          claudeActivation = pkgs.writeText "claude-mcp-activation" hm.home.activation.claudeMcpServers.data;
          devboxEndpoints = builtins.toJSON self.homeConfigurations.devbox.config.wgn.home.mcp-proxy.endpoints;
          agentConfig = hm.xdg.configFile."agent-tools/config.json".source;
        } ''
          python ${./overlays/pdfm-mcp/check.py} "$binary" "$inventory" ${./overlays/agent-tools/internal/guard/testdata/tools/parallels.json}
          python ${./modules/home-manager/mcp-proxy/parallels-tests.py}
          touch $out
        '';

      # The ngrok Mac's mcp-proxy-authorize restarts the proxy, and loads
      # its plist when kickstart finds it loaded in no domain; a failed load
      # fails it.
      checks.${darwinOnly "mcp-proxy-restart"} = let
        authorize =
          builtins.head (builtins.filter (p: (p.name or "") == "mcp-proxy-authorize")
            self.darwinConfigurations.ngrok.config.home-manager.users.${darwinSystems.ngrok.username}.home.packages);
      in
        pkgs.runCommand "mcp-proxy-restart" {
          restart = pkgs.lib.getExe authorize.restart;
          inherit (authorize) plist;
        } ''
          bash ${./modules/home-manager/mcp-proxy/restart-tests.sh}
          touch $out
        '';

      checks.${darwinOnly "git-fetch-urls"} = let
        git = self.darwinConfigurations.ngrok.config.home-manager.users.${darwinSystems.ngrok.username}.programs.git;
      in
        pkgs.runCommand "git-fetch-urls" {
          nativeBuildInputs = [pkgs.git pkgs.jq];
          includes = builtins.toJSON (builtins.filter (inc: (inc.condition or null) != null) git.includes);
          helper = git.settings.credential."https://github.com".helper;
          operatorHome = self.darwinConfigurations.ngrok.config.home-manager.users.${darwinSystems.ngrok.username}.home.homeDirectory;
          passAsFile = ["includes"];
        } ''
          bash ${./modules/home-manager/git/fetch-urls-tests.sh}
          touch $out
        '';

      # No Home Manager launchd agent of any Mac sets
      # LimitLoadToSessionType, which Home Manager sets to Background for an
      # agent in the user domain. There launchd did not load mcp-proxy after
      # a reboot, and it stayed down until loaded by hand; agents stay in the
      # default gui domain.
      checks.launchd-agents = let
        inherit (pkgs) lib;
        limited = lib.concatLists (lib.mapAttrsToList (machine: system:
          lib.concatLists (lib.mapAttrsToList (user: home:
            map (agent: "${machine}/${user}/${agent}") (builtins.attrNames
              (lib.filterAttrs (_: a: a.enable && a.config.LimitLoadToSessionType != null) home.launchd.agents)))
          system.config.home-manager.users))
        self.darwinConfigurations);
      in
        pkgs.runCommand "launchd-agents" {
          limited = builtins.concatStringsSep " " limited;
        } ''
          if [ -n "$limited" ]; then
            echo "launchd-agents: these set LimitLoadToSessionType (domain user?): $limited" >&2
            exit 1
          fi
          touch $out
        '';

      # The ngrok Mac raises launchd's maxfiles at load, which a GUI Emacs
      # inherits at 256 by default; kqueue spends one descriptor per watched
      # file.
      checks.${darwinOnly "emacs-maxfiles"} = let
        daemon = self.darwinConfigurations.ngrok.config.launchd.daemons.limit-maxfiles.serviceConfig or {};
      in
        pkgs.runCommand "emacs-maxfiles" {
          command = builtins.concatStringsSep " " (daemon.ProgramArguments or []);
          runAtLoad = toString (daemon.RunAtLoad or false);
        } ''
          want="/bin/launchctl limit maxfiles 65536 524288"
          if [ "$command" != "$want" ] || [ "$runAtLoad" != 1 ]; then
            echo "emacs-maxfiles: got '$command' (RunAtLoad=$runAtLoad), want '$want' at load" >&2
            exit 1
          fi
          touch $out
        '';

      # The pstack skills as the ngrok Mac links them each carry pstack's
      # licence, and the Codex policy that keeps a skill from being invoked
      # unasked exactly when its SKILL.md keeps Claude Code from it.
      # statechart has neither, so agents can run it on their own, and its
      # SKILL.md no longer sends them to mise. It runs with no network on
      # one of its own corpus specs, as its corpus.test.ts runs them: it
      # exits 1 for the rule
      # that breaks, writes nothing to stderr, its chart and report are the
      # committed ones, and the chart renders with the node and Pele that
      # its references/render.md names. Built for aarch64-darwin, the
      # system of the configuration it reads.
      checks.${darwinOnly "pstack-skills"} = let
        skills = self.darwinConfigurations.ngrok.config.home-manager.users.${darwinSystems.ngrok.username}.wgn.home.llm.skills;
        statechart = skills.statechart.source;
        # What pstackSkill reads to decide the Codex policy.
        disablesModelInvocation = pkgs.callPackage ./modules/home-manager/llm/disables-model-invocation.nix {};
        # The ngrok Mac's builder, run here on each case below.
        pstackSkill = import ./modules/home-manager/llm/pstack-skill.nix {inherit cursor-plugins pkgs;};
        # SKILL.md files, and what the read makes of each: "disabled" or
        # "not-disabled" through pstackSkill, whose agents/openai.yaml this
        # checks, or "error" for one whose frontmatter is not plain, which
        # the read refuses with status 2 and pstackSkill fails to build on.
        frontmatter = let
          flag = "disable-model-invocation";
          text = pkgs.writeText "SKILL.md";
          skill = lines: text "---\nname: x\n${lines}\n---\nbody\n";
          # For bytes a Nix string does not hold: printf's escapes.
          bytes = format: pkgs.runCommand "SKILL.md" {} "printf -- '${format}' > $out";
        in {
          plain = {
            expect = "disabled";
            file = skill "${flag}: true";
          };
          capitalised = {
            expect = "disabled";
            file = skill "${flag}: True";
          };
          yes = {
            expect = "disabled";
            file = skill "${flag}: yes";
          };
          on = {
            expect = "disabled";
            file = skill "${flag}: ON";
          };
          commented-value = {
            expect = "disabled";
            file = skill "${flag}: true # no auto";
          };
          trailing-space-value = {
            expect = "disabled";
            file = skill "${flag}: true ";
          };
          after-block-scalar = {
            expect = "disabled";
            file = skill "description: |\n  x\n${flag}: true";
          };
          byte-order-mark = {
            expect = "disabled";
            file = bytes "\\357\\273\\277---\\nname: x\\n${flag}: true\\n---\\nbody\\n";
          };
          crlf = {
            expect = "disabled";
            file = bytes "---\\r\\nname: x\\r\\n${flag}: true\\r\\n---\\r\\nbody\\r\\n";
          };
          trailing-space-opener = {
            expect = "disabled";
            file = text "--- \nname: x\n${flag}: true\n---\nbody\n";
          };
          trailing-space-closer = {
            expect = "disabled";
            file = text "---\nname: x\n${flag}: true\n--- \nbody\n";
          };
          closer-at-end = {
            expect = "disabled";
            file = text "---\n${flag}: true\n---";
          };
          plain-false = {
            expect = "not-disabled";
            file = skill "${flag}: false";
          };
          false-later = {
            expect = "not-disabled";
            file = skill "${flag}: true\n${flag}: false";
          };
          absent = {
            expect = "not-disabled";
            file = skill "description: x";
          };
          commented-out = {
            expect = "not-disabled";
            file = skill "# ${flag}: true";
          };
          nested = {
            expect = "not-disabled";
            file = skill "metadata:\n  ${flag}: true";
          };
          in-body = {
            expect = "not-disabled";
            file = text "---\nname: x\n---\n\n${flag}: true\n";
          };
          no-opener = {
            expect = "not-disabled";
            file = text "# x\n${flag}: true\n---\n";
          };
          later-block = {
            expect = "not-disabled";
            file = text "# x\n\n---\n${flag}: true\n---\n";
          };
          blank-first-line = {
            expect = "not-disabled";
            file = text "\n---\n${flag}: true\n---\nbody\n";
          };
          empty = {
            expect = "not-disabled";
            file = text "";
          };
          double-quoted = {
            expect = "error";
            file = skill "${flag}: \"true\"";
          };
          single-quoted = {
            expect = "error";
            file = skill "${flag}: 'true'";
          };
          one = {
            expect = "error";
            file = skill "${flag}: 1";
          };
          one-point-oh = {
            expect = "error";
            file = skill "${flag}: 1.0";
          };
          y = {
            expect = "error";
            file = skill "${flag}: y";
          };
          no-value = {
            expect = "error";
            file = skill "${flag}:";
          };
          no-space = {
            expect = "error";
            file = skill "${flag}:true";
          };
          unquoted-colon = {
            expect = "error";
            file = skill "description: Use it for: anything\n${flag}: false";
          };
          tab-indented = {
            expect = "error";
            file = skill "\t${flag}: true";
          };
          never-closed = {
            expect = "error";
            file = text "---\nname: x\n${flag}: true\n";
          };
          four-dash-closer = {
            expect = "error";
            file = text "---\n${flag}: true\n----\nbody\n";
          };
          dots-closer = {
            expect = "error";
            file = text "---\n${flag}: true\n...\nbody\n";
          };
          not-a-mapping = {
            expect = "error";
            file = text "---\n- ${flag}\n---\n";
          };
          empty-frontmatter = {
            expect = "error";
            file = text "---\n---\nbody\n";
          };
          not-utf-8 = {
            expect = "error";
            file = bytes "---\\nname: x\\n---\\nbody \\377\\n";
          };
          # Claude Code's own pattern closes the block at a `---` anywhere.
          dashes-mid-line = {
            expect = "error";
            file = skill "description: a---b\n${flag}: false";
          };
          dashes-quoted = {
            expect = "error";
            file = skill "description: \"Check it --- then draw\"\n${flag}: false";
          };
          # pyyaml raises ValueError, not a YAMLError, for a date that is not.
          invalid-date = {
            expect = "error";
            file = skill "description: 2026-99-99\n${flag}: true";
          };
          lone-cr = {
            expect = "error";
            file = bytes "---\\nname: x\\r${flag}: true\\n---\\nbody\\n";
          };
          next-line = {
            expect = "error";
            file = bytes "---\\nname: x\\302\\205${flag}: true\\n---\\nbody\\n";
          };
          line-separator = {
            expect = "error";
            file = bytes "---\\nname: x\\342\\200\\250${flag}: true\\n---\\nbody\\n";
          };
          paragraph-separator = {
            expect = "error";
            file = bytes "---\\nname: x\\342\\200\\251${flag}: true\\n---\\nbody\\n";
          };
        };
        # pstackSkill fails to build on a SKILL.md the read refuses.
        refused = pkgs.testers.testBuildFailure (pstackSkill "create-formal-verification-skill" ''
          cat ${frontmatter.double-quoted.file} > SKILL.md
        '');
        corpus = "${statechart}/scripts/test/corpus/TwoPhase";
        render = pkgs.writeText "render.mjs" ''
          import fs from "node:fs";
          const [pele, mmd] = process.argv.slice(2);
          const { render } = await import(pele);
          const { svg } = render(fs.readFileSync(mmd, "utf8"), {
            limit: 1_000_000,
            outputLimit: 8_000_000,
            maxEdges: 10_000,
          });
          if (!svg.startsWith("<svg")) throw new Error(`not an SVG: ''${svg.slice(0, 80)}`);
        '';
      in
        pkgs.runCommand "pstack-skills" {} ''
          export HOME=$TMPDIR
          failed=
          ${pkgs.lib.concatStrings (pkgs.lib.mapAttrsToList (name: case:
            if case.expect == "error"
            then ''
              status=0
              ${disablesModelInvocation} ${case.file} 2>/dev/null || status=$?
              if [ "$status" != 2 ]; then
                echo "pstack-skills: the ${name} SKILL.md is read with status $status, not 2 for frontmatter that is not plain" >&2
                failed=1
              fi
            ''
            else let
              skill = pstackSkill "create-formal-verification-skill" ''
                cat ${case.file} > SKILL.md
              '';
            in ''
              if [ -e ${skill}/agents/openai.yaml ]; then
                got=disabled
              else
                got=not-disabled
              fi
              if [ "$got" != ${case.expect} ]; then
                echo "pstack-skills: pstackSkill makes the ${name} SKILL.md $got, not ${case.expect}" >&2
                failed=1
              fi
            '')
          frontmatter)}
          status=0
          ${disablesModelInvocation} "$TMPDIR/missing/SKILL.md" 2>/dev/null || status=$?
          if [ "$status" != 2 ]; then
            echo "pstack-skills: a missing SKILL.md is read with status $status, not 2" >&2
            failed=1
          fi
          if [ "$(cat ${refused}/testBuildFailure.exit)" != 2 ] ||
            ! grep -q '^disables-model-invocation: SKILL.md: ' ${refused}/testBuildFailure.log; then
            echo "pstack-skills: pstackSkill on a refused SKILL.md fails other than with the read's status 2:" >&2
            cat ${refused}/testBuildFailure.log >&2
            failed=1
          fi
          [ -z "$failed" ]
          for skill in ${toString (map (skill: skill.source) (builtins.filter (skill: skill.source.pstack or false) (builtins.attrValues skills)))}; do
            if [ ! -f "$skill/LICENSE" ]; then
              echo "pstack-skills: $skill has no LICENSE" >&2
              exit 1
            fi
            status=0
            ${disablesModelInvocation} "$skill/SKILL.md" || status=$?
            case $status in
              0)
                if ! grep -qx '  allow_implicit_invocation: false' "$skill/agents/openai.yaml"; then
                  echo "pstack-skills: $skill lets Codex invoke it unasked" >&2
                  exit 1
                fi
                ;;
              1)
                if [ -e "$skill/agents/openai.yaml" ]; then
                  echo "pstack-skills: $skill keeps Codex from invoking it, but not Claude Code" >&2
                  exit 1
                fi
                ;;
              *) exit 1 ;;
            esac
          done
          status=0
          ${disablesModelInvocation} ${statechart}/SKILL.md || status=$?
          if [ "$status" != 1 ]; then
            echo "pstack-skills: agents cannot invoke statechart: the read gives $status" >&2
            exit 1
          fi
          if grep -q mise ${statechart}/SKILL.md; then
            echo "pstack-skills: statechart's SKILL.md mentions mise" >&2
            exit 1
          fi

          status=0
          ${statechart}/scripts/statechart ${corpus}/TwoPhase.tla \
            --view ${corpus}/view.mjs --out out 2> stderr || status=$?
          if [ "$status" != 1 ]; then
            echo "pstack-skills: statechart exited $status, want 1: TCConsistent breaks" >&2
            exit 1
          fi
          if [ -s stderr ]; then
            echo "pstack-skills: statechart wrote to stderr:" >&2
            cat stderr >&2
            exit 1
          fi
          diff ${corpus}/chart.mmd out/chart.mmd
          diff ${corpus}/report.md out/report.md

          node=$(grep -o '/nix/store/[^`]*/bin/node' ${statechart}/references/render.md) || {
            echo "pstack-skills: render.md names no node in the store" >&2
            exit 1
          }
          pele=$(grep -o 'file:///nix/store/[^`]*/pele\.js' ${statechart}/references/render.md) || {
            echo "pstack-skills: render.md names no Pele in the store" >&2
            exit 1
          }
          "$node" ${render} "$pele" out/chart.mmd
          touch $out
        '';

      checks.${darwinOnly "codex-subagent-routing"} = let
        username = darwinSystems.ngrok.username;
        homeFor = gateway:
          (self.darwinConfigurations.ngrok.extendModules {
            modules = [
              {
                home-manager.users.${username}.wgn.home.llm = {
                  gateway = pkgs.lib.mkForce gateway;
                  ngrokAi.models.fast = pkgs.lib.mkForce "gateway-fast-test";
                };
              }
            ];
          }).config.home-manager.users.${
            username
          };
        native = homeFor "none";
        gateway = homeFor "ngrok-ai";
        candidate = home: home.home.file.".codex/cheap-model".source or (pkgs.writeText "missing-cheap-model" "");
        roles = pkgs.lib.filterAttrs (name: _: pkgs.lib.hasPrefix ".codex/agents/" name) native.home.file;
      in
        pkgs.runCommand "codex-subagent-routing" {
          nativeBuildInputs = [pkgs.python3];
          nativeModel = candidate native;
          gatewayModel = candidate gateway;
          roleFiles = builtins.toJSON (pkgs.lib.mapAttrsToList (_: file: toString file.source) roles);
        } ''
          python3 - <<'PY'
          import json
          import os
          import pathlib
          import tomllib

          for key, expected in [("nativeModel", "gpt-6-luna"), ("gatewayModel", "gateway-fast-test")]:
              actual = pathlib.Path(os.environ[key]).read_text().strip()
              assert actual == expected, f"{key}: expected {expected!r}, got {actual!r}"
          roles = [tomllib.loads(pathlib.Path(p).read_text()) for p in json.loads(os.environ["roleFiles"])]
          assert any(role["name"] == "builder" for role in roles), "builder role missing"
          assert any(role["name"] == "adversarial-reviewer" for role in roles), "reviewer role missing"
          for role in roles:
              assert "model" not in role, f"{role['name']}: model pin defeats task routing"
              assert role["model_reasoning_effort"] == "high", f"{role['name']}: effort changed"
          PY
          touch $out
        '';

      # The Claude Code settings the ngrok Mac's home configuration writes
      # hold the permission rules beside the guard: bypass mode off; MCP
      # denials only by claude.ai connector name, never of the connectors
      # kept, so no local server is denied; for
      # every server mcp.json lists, each read allowed and each confirm
      # asked, never allowed; no allow rule that a deny rule covers, which
      # deny would override; the guard's PreToolUse hook matching MCP
      # tools, every hook of it blocking when it cannot run. In these and
      # in the headless reviewers' settings, telemetry exported to the
      # collector the ngrok Mac runs and kept on the host, with no
      # OTEL_LOG_* content logging, no traces, otlp the only exporter, and
      # every OTLP endpoint on loopback.
      checks.${darwinOnly "claude-settings"} = let
        ngrok = darwinSystems.ngrok;
        home = self.darwinConfigurations.ngrok.config.home-manager.users.${ngrok.username};
      in
        pkgs.runCommand "claude-settings" {
          nativeBuildInputs = [pkgs.jq];
          settings = builtins.toJSON home.wgn.home.claude.settings;
          codex = builtins.toJSON home.wgn.home.codex.sandbox;
          mcp = builtins.readFile ./overlays/agent-tools/internal/guard/mcp.json;
          passAsFile = ["settings" "mcp" "codex"];
          reviewerSettings = home.wgn.home.llm.delegate.settings;
          otelUrl = home.wgn.home.otel-collector.url;
        } ''
          s=$settingsPath
          label=
          # Each check prints what breaks it, and fails on any.
          check() {
            found=$(jq -r "$@" "$s")
            if [ -n "$found" ]; then
              printf '%s\n' "$found" | sed "s|^|$label|" >&2
              failed=1
            fi
          }
          failed=
          for domain in rubygems.org api.nuget.org repo.packagist.org repo.hex.pm plugins.gradle.org plugins-artifacts.gradle.org formulae.brew.sh ghcr.io; do
            check --arg domain "$domain" 'if (.sandbox.network.allowedDomains | index($domain)) != null then empty
              else "registry not allowed in Claude: \($domain)" end'
            if ! jq -e --arg domain "$domain" '.permissions.wgn.network.domains[$domain] == "allow"' "$codexPath" > /dev/null; then
              echo "registry not allowed in Codex: $domain" >&2
              failed=1
            fi
          done
          check 'if (.sandbox.network.allowedDomains | index("repo1.maven.org")) != null then empty
            else "Maven Central is not allowed in Claude" end'
          check 'if (.sandbox.network.allowedDomains | index("repo.maven.apache.org")) != null then empty
            else "Maven default repository is not allowed in Claude" end'
          if ! jq -e '.permissions.wgn.network.domains["repo1.maven.org"] == "allow"' "$codexPath" > /dev/null; then
            echo 'Maven Central is not allowed in Codex' >&2
            failed=1
          fi
          if ! jq -e '.permissions.wgn.network.domains["repo.maven.apache.org"] == "allow"' "$codexPath" > /dev/null; then
            echo 'Maven default repository is not allowed in Codex' >&2
            failed=1
          fi
          check 'if .permissions.disableBypassPermissionsMode == "disable" then empty
            else "bypass-permissions mode is not disabled" end'
          check '.deniedMcpServers[] | select((keys != ["serverName"]) or (.serverName | startswith("claude.ai ") | not))
            | "deny entry is not a claude.ai connector name: \(tojson)"'
          check '.deniedMcpServers as $d | ["claude.ai Claude Docs", "claude.ai ngrok Debug (prod)", "claude.ai ngrok Deploys"][]
            | select(. as $n | $d | any(.serverName == $n)) | "kept connector is denied: \(.)"'
          check 'if (.sandbox.excludedCommands | index("agent-plan link *")) != null then empty
            else "agent-plan link is not excluded from the sandbox" end'
          check 'if (.permissions.allow | index("Bash(agent-plan link *)")) != null then empty
            else "agent-plan link is not allowed" end'
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
          for s in "$settingsPath" "$reviewerSettings"; do
            [ "$s" = "$settingsPath" ] && label="settings: " || label="reviewer settings: "
            check --arg url "$otelUrl" '(.env // {}) as $env
              | {CLAUDE_CODE_ENABLE_TELEMETRY: "1", OTEL_METRICS_EXPORTER: "otlp", OTEL_LOGS_EXPORTER: "otlp",
                  OTEL_EXPORTER_OTLP_PROTOCOL: "http/protobuf",
                  OTEL_EXPORTER_OTLP_METRICS_ENDPOINT: "\($url)/v1/metrics", OTEL_EXPORTER_OTLP_LOGS_ENDPOINT: "\($url)/v1/logs"}
              | to_entries[] | select($env[.key] != .value) | "telemetry not sent to the collector: \(.key)"'
            # The collector receives OTLP over HTTP alone.
            check '.env // {} | to_entries[] | select(.key | test("^OTEL_EXPORTER_OTLP_[A-Z]+_PROTOCOL$")) | select(.value != "http/protobuf")
              | "OTLP protocol other than http/protobuf: \(.key)=\(.value)"'
            check '.env // {} | keys[] | select(startswith("OTEL_LOG_")) | "telemetry logs content: \(.)"'
            check '.env // {} | keys[] | select(IN("OTEL_TRACES_EXPORTER", "BETA_TRACING_ENDPOINT", "ENABLE_BETA_TRACING_DETAILED",
                "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA", "ENABLE_ENHANCED_TELEMETRY_BETA")) | "telemetry traces: \(.)"'
            check '.env // {} | to_entries[] | select(.key | IN("OTEL_METRICS_EXPORTER", "OTEL_LOGS_EXPORTER")) | select(.value != "otlp")
              | "telemetry exporter other than otlp: \(.key)=\(.value)"'
            check '.env // {} | to_entries[] | select(.key | test("^OTEL_EXPORTER_OTLP_([A-Z]+_)?ENDPOINT$"))
              | select(.value | test("^https?://(127\\.0\\.0\\.1|localhost|\\[::1\\])(:[0-9]+)?(/|$)") | not)
              | "OTLP endpoint off the host: \(.key)=\(.value)"'
          done
          [ -z "$failed" ]
          touch $out
        '';

      # The ngrok Mac's collector config: the collector accepts it, it
      # receives OTLP on 127.0.0.1 alone, serves none of its own metrics,
      # and writes metrics and logs, and nothing else, each to a rotated
      # file.
      checks.${darwinOnly "otel-collector"} = let
        home = self.darwinConfigurations.ngrok.config.home-manager.users.${darwinSystems.ngrok.username};
        # The launchd agent's last argument is the config file.
        config = pkgs.lib.last home.launchd.agents.otel-collector.config.ProgramArguments;
      in
        pkgs.runCommand "otel-collector" {
          nativeBuildInputs = [pkgs.opentelemetry-collector-contrib pkgs.yq-go pkgs.jq];
        } ''
          export HOME=$TMPDIR
          otelcol-contrib validate --config ${config}
          yq -o=json ${config} > config.json
          # Each check prints what breaks it, and fails on any.
          check() {
            found=$(jq -r "$@" config.json)
            if [ -n "$found" ]; then
              printf '%s\n' "$found" >&2
              failed=1
            fi
          }
          failed=
          check 'if (.receivers | keys) == ["otlp"] then empty else "receivers other than otlp: \(.receivers | keys)" end'
          check '.receivers.otlp.protocols | to_entries[] | select((.value.endpoint // "") | startswith("127.0.0.1:") | not)
            | "receiver off loopback: \(.key) \(.value.endpoint)"'
          check 'if .service.telemetry.metrics.level == "none" then empty else "the collector serves its own metrics" end'
          check 'if .extensions == null and .connectors == null and (.service.extensions // []) == [] then empty
            else "extensions or connectors are set" end'
          check 'if (.service.pipelines | keys) == ["logs", "metrics"] then empty
            else "pipelines are not exactly logs and metrics: \(.service.pipelines | keys)" end'
          check '.service.pipelines | to_entries[] | .key as $p | .value.exporters[] | select(startswith("file/") | not)
            | "pipeline \($p) exports to \(.)"'
          check '.exporters | to_entries[]
            | select((.key | startswith("file/") | not) or (.value.rotation // {} | .max_megabytes and .max_backups and .max_days | not))
            | "exporter \(.key) is not a rotated file"'
          [ -z "$failed" ]
          touch $out
        '';

      # Every line of a shell code block, fenced or indented, and every
      # inline code span in the skills, the AGENTS files and CHECKS.md,
      # judged by the guard as a Claude Code Bash call with the ngrok Mac's excluded
      # commands: none is refused for naming an excluded command anywhere
      # but alone, or with syntax that sends it sandboxed even inside
      # quotes, or for running a read of gh's that gh-read runs, so no
      # instruction hands an agent a line its sandbox fails. A
      # <PLACEHOLDER> is read as a word, not a redirection.
      checks.skill-commands = let
        ngrok = darwinSystems.ngrok;
        excluded = self.darwinConfigurations.ngrok.config.home-manager.users.${ngrok.username}.wgn.home.llm.guard.excludedCommands;
        docs = pkgs.lib.fileset.toSource {
          root = ./.;
          fileset = pkgs.lib.fileset.unions [
            ./modules/home-manager/llm/skills
            ./modules/home-manager/llm/AGENTS.md
            ./modules/home-manager/llm/AGENTS.codex.md
            ./AGENTS.md
            ./CHECKS.md
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
          # or indented, continuations joined, and each inline code span.
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
              # A line of a shell block, joined to the lines it continues
              # onto with a trailing backslash.
              function shell(line) {
                sub(/^[ \t]+/, "", line)
                if (pending == "") { start = FNR; pendfile = FILENAME } else { line = pending " " line; pending = "" }
                if (line ~ /\\$/) { sub(/\\$/, "", line); pending = line; return }
                emit(line, FILENAME, start)
              }
              FNR == 1 { flush(); fence = ""; kind = ""; list = -1; blank = 1; code = 0 }
              # A fence closes only on a run of its own character at least
              # as long as the one that opened it, with nothing after.
              /^[ \t]*(```|~~~)/ {
                mark = $0; sub(/^[ \t]*/, "", mark)
                ch = substr(mark, 1, 1); run = mark; sub("[^" ch "].*", "", run)
                if (fence != "") {
                  if (substr(fence, 1, 1) == ch && length(run) >= length(fence) && mark ~ ("^" run "[ \t]*$")) {
                    flush(); fence = ""; kind = ""; blank = 1
                  }
                  next
                }
                flush(); code = 0
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
              kind == "shell" { shell($0); next }
              kind != "" { next }
              # Outside a fence. A list item sets the column its content
              # starts at, and a line indented less than that after a
              # blank line ends the list.
              {
                line = $0; gsub(/\t/, "    ", line)
                if (line ~ /^ *$/) { blank = 1; next }
                match(line, /^ */); indent = RLENGTH
                base = (list >= 0) ? list : 0
                # An indented code block starts after a blank line, four
                # columns past the content of the list it is in, if any.
                if (indent >= base + 4 && (blank || code)) {
                  code = 1; blank = 0; shell(line); next
                }
                if (code) flush()
                code = 0
                if (match(line, /^ *([-*+]|[0-9]+[.)])( +|$)/)) {
                  list = RLENGTH
                } else if (blank && indent < base) {
                  list = -1
                }
                blank = 0
                # Each code span: a run of backticks, up to the next run
                # of the same length, with one space trimmed from each end
                # of what it holds where both have one.
                rest = line
                while (match(rest, /`+/)) {
                  n = RLENGTH; rest = substr(rest, RSTART + n); span = ""; closed = 0
                  while (match(rest, /`+/)) {
                    span = span substr(rest, 1, RSTART - 1)
                    if (RLENGTH == n) { rest = substr(rest, RSTART + n); closed = 1; break }
                    span = span substr(rest, RSTART, RLENGTH); rest = substr(rest, RSTART + RLENGTH)
                  }
                  if (!closed) break
                  if (span ~ /^ .* $/) span = substr(span, 2, length(span) - 2)
                  emit(span, FILENAME, FNR)
                }
              }
              END { flush() }' "$@"
          }
          failed=
          while IFS=$'\t' read -r where line; do
            reason=$(jq -cn --arg c "$line" '{tool_name: "Bash", cwd: "/", tool_input: {command: $c}}' |
              pr-loop guard --baseline | jq -r '.hookSpecificOutput.permissionDecisionReason // empty')
            case "$reason" in
              *"only as a command line of its own"* | *"only when Claude Code reads its line as that command alone"* | \
              '`gh` runs in the sandbox'*)
                printf '%s: %s\n  %s\n' "$where" "$line" "$reason" >&2
                failed=1
                ;;
            esac
          done < <(lines $(find modules/home-manager/llm/skills -name '*.md' | sort) modules/home-manager/llm/AGENTS.md modules/home-manager/llm/AGENTS.codex.md AGENTS.md CHECKS.md)
          [ -z "$failed" ]
          touch $out
        '';

      # trash-scratch-tests.sh against the ngrok Mac's trash-scratch, which
      # asks agent-plan as the overlay builds it for the session's plan
      # worktrees; the package's postCheck runs the same tests. devbox's
      # Linux trash-scratch needs a builder this Mac lacks.
      checks.${darwinOnly "trash-scratch"} = let
        home = self.darwinConfigurations.ngrok.config.home-manager.users.${darwinSystems.ngrok.username};
      in
        (pkgs.lib.findFirst (p: (p.name or "") == "trash-scratch")
          (throw "trash-scratch: not in the ngrok Mac's home.packages")
          home.home.packages).passthru.tests;

      # The agent-tools config.json the ngrok Mac's home configuration writes
      # holds what agent-plan and emacs-config's plan views read from it:
      # plans.dir and plans.repo, absolute, the directory inside the
      # repository, and plans.host. Then agent-plan, unbaked, makes a plan
      # with it, its home moved under the build's directory so that nothing
      # outside it is read or written.
      checks.${darwinOnly "agent-tools-config"} = let
        ngrok = darwinSystems.ngrok;
        home = self.darwinConfigurations.ngrok.config.home-manager.users.${ngrok.username};
      in
        pkgs.runCommand "agent-tools-config" {
          nativeBuildInputs = [pkgs.jq (pkgs.callPackage ./overlays/agent-tools {})];
          config = home.xdg.configFile."agent-tools/config.json".source;
          inherit (home.home) homeDirectory;
        } ''
          jq -e '.plans | . as $p | ($p.dir | type == "string" and startswith("/"))
            and ($p.repo | type == "string" and startswith("/"))
            and ($p.dir | startswith($p.repo + "/")) and ($p.host | type == "string" and length > 0)' \
            "$config" >/dev/null || {
            echo "agent-tools-config: plans lacks an absolute dir inside an absolute repo, or a host:" >&2
            jq .plans "$config" >&2
            exit 1
          }
          export HOME=$TMPDIR/home AGENT_TOOLS_CONFIG=$TMPDIR/config.json
          jq --arg from "$homeDirectory" --arg to "$HOME" \
            'walk(if type == "string" and startswith($from) then $to + .[($from | length):] else . end)' \
            "$config" > "$AGENT_TOOLS_CONFIG"
          made=$(agent-plan new --title check --slug check)
          case "$(jq -r .plan <<<"$made")" in
            "$(jq -r .plans.dir "$AGENT_TOOLS_CONFIG")"/*) ;;
            *) echo "agent-tools-config: agent-plan made $made outside plans.dir" >&2; exit 1 ;;
          esac
          touch $out
        '';

      # Security and complexity lint over agent-tools, with the rules in
      # overlays/agent-tools/.golangci.yml; .github/workflows/agent-tools-lint.yml
      # runs it. Run it with `nix build .#checks.<system>.agent-tools-lint`.
      # It is kept out of the package's check phase so that a new lint rule
      # fails CI and never a switch. It runs without network, on the module
      # dependencies the package vendors (its vendorHash).
      checks.agent-tools-lint =
        pkgs.runCommand "agent-tools-lint" {
          nativeBuildInputs = [pkgs.go pkgs.golangci-lint pkgs.python3];
          src = pkgs.lib.fileset.toSource {
            root = ./overlays/agent-tools;
            fileset = pkgs.lib.fileset.unions [
              ./overlays/agent-tools/.golangci.yml
              ./overlays/agent-tools/lint-tests.py
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
          python3 lint-tests.py .golangci.yml
          golangci-lint run ./...
          touch $out
        '';

      # Every check of every system builds only derivations of that system
      # or Nix's builtin ones, such as fetchurl, so a check that reads a
      # darwin configuration is darwinOnly. It reads the .drv file of each
      # derivation in each check's closure, following each one's input
      # derivations and building none of them, and names each check that
      # builds for another system, with how many derivations of which. A
      # check that does not evaluate builds nothing and is left out: nixpkgs
      # throws for x86_64-darwin, which it dropped. It walks the checks of
      # every system, so one system runs it for all, the one this Mac
      # builds.
      checks.${darwinOnly "check-systems"} = let
        listed =
          pkgs.lib.concatLists (pkgs.lib.mapAttrsToList (checkSystem: checks:
            pkgs.lib.concatLists (pkgs.lib.mapAttrsToList (name: check: let
              drvPath = builtins.tryEval check.drvPath;
            in
              pkgs.lib.optional drvPath.success {
                inherit checkSystem name;
                # The .drv file alone, whose closure of .drv files and sources
                # the build can read: its drvPath would also make every output
                # in its closure an input, whatever system builds it, as would
                # exportReferencesGraph.
                drv = builtins.unsafeDiscardOutputDependency drvPath.value;
              }) (builtins.removeAttrs checks ["check-systems"])))
          self.checks)
          ++ [
            {
              # A derivation with no input derivation, as a check built by
              # builtin:fetchurl would be, passes.
              checkSystem = system;
              name = "a leaf derivation";
              drv =
                builtins.unsafeDiscardOutputDependency
                (derivation {
                  name = "check-systems-leaf";
                  inherit system;
                  builder = "/bin/sh";
                }).drvPath;
            }
          ];
      in
        pkgs.runCommand "check-systems" {
          nativeBuildInputs = [pkgs.coreutils pkgs.findutils pkgs.gnugrep];
          listed = pkgs.lib.concatMapStrings (c: "${c.drv} ${c.checkSystem} ${c.name}\n") listed;
          passAsFile = ["listed"];
        } ''
          # A .drv file is one line, Derive(outputs,inputDrvs,inputSrcs,
          # "system",...): each input derivation is a ("PATH.drv",[ and the
          # system follows the first `],"`.
          closure() {
            echo "$1" > seen
            cp seen frontier
            while [ -s frontier ]; do
              # grep finds nothing in a frontier of derivations that have
              # no input derivation, which is no error.
              xargs cat < frontier |
                { grep -oP '\("\K/nix/store/[^"]+\.drv(?=",\[)' || [ $? = 1 ]; } |
                sort -u | comm -23 - seen > next
              sort -m -o seen seen next
              mv next frontier
            done
            cat seen
          }
          failed=
          while read -r drv checkSystem name; do
            closure "$drv" > drvs
            xargs grep -ohP '^Derive\(.*?\],"\K[^"]*(?=",")' < drvs > systems
            if [ "$(wc -l < systems)" != "$(wc -l < drvs)" ]; then
              echo "check-systems: read no system from some of the .drv files of checks.$checkSystem.$name" >&2
              failed=1
            fi
            strays=$({ grep -vx -e "$checkSystem" -e builtin systems || true; } | sort | uniq -c | xargs)
            if [ -n "$strays" ]; then
              echo "check-systems: checks.$checkSystem.$name builds derivations of another system (count, system): $strays" >&2
              failed=1
            fi
          done < "$listedPath"
          [ -z "$failed" ]
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
