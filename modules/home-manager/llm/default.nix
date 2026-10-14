{
  config,
  emacs-skills,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.llm;

  # d2 renders a PNG through a Chromium it downloads at run time, after asking
  # on the terminal, which an agent cannot answer and a sandbox cannot
  # download. So a PNG is rendered as SVG, which needs no browser, and
  # converted.
  d2Png = pkgs.writeShellApplication {
    name = "d2";
    runtimeInputs = [pkgs.d2 pkgs.librsvg];
    text = ''
      out="''${*: -1}"
      if [[ $# -lt 2 || "$out" != *.png ]]; then
        exec d2 "$@"
      fi
      tmp="$(mktemp -d)"
      trap 'rm -rf "$tmp"' EXIT
      d2 "''${@:1:$#-1}" "$tmp/diagram.svg"
      rsvg-convert -f png -o "$out" "$tmp/diagram.svg"
    '';
  };
in {
  options.wgn.home.llm = {
    enable = mkEnableOption "Enables the plumbing shared by my coding agents";

    instructions = mkOption {
      type = types.path;
      default = ./AGENTS.md;

      description = ''
        Working agreements every enabled coding agent loads into each session,
        linked to wherever that agent reads its user-level instructions.
      '';
    };

    prLoop = {
      enable = mkEnableOption ''
        the `pr-loop` command and `review-loop` skill, through which an
        unattended review loop pushes, replies and requests reviews on
        GitHub. Run directly, it uses the `gh` login, so its policy guards
        an agent that forgets a rule but does not stop one that runs `gh`
        itself. Run through `sandbox-relay` for a sandbox without
        credentials, it is the only way out
      '';

      bots = mkOption {
        type = types.attrsOf types.attrs;

        description = ''
          Review bots the loop requests, waits for and answers, by name.

          - `login`: regex matching the bot's GitHub login
          - `request`: `{ reviewer = "@login"; }` to request it as a
            reviewer, or `{ comment = "..."; }` to post a top-level trigger
            comment, which the loop deletes once superseded
          - `verdicts` (optional): verdict name to text found in the bot's
            review body
          - `resolvesOwnThreads` (optional): the bot resolves its threads
            itself when it re-reviews, so the loop leaves fixed ones to it
          - `repos` (optional): `owner/name` globs it is limited to
        '';

        default = {
          copilot = {
            login = "^copilot";
            request.reviewer = "@copilot";
            verdicts = {
              approval = "Approval recommended";
              changes = "Changes recommended";
              closer-look = "Needs a closer look";
              error = "encountered an error";
            };
          };

          # Reviews once on its own when a PR opens or leaves draft, and never
          # on push. A bare @-mention gets a chat reply rather than a review.
          qodo = {
            login = "^qodo-code-review";
            request.comment = "/agentic_review";
            resolvesOwnThreads = true;
          };
        };
      };

      setup = mkOption {
        type = types.attrsOf types.str;
        default = {};
        example = {"owner/name" = "go tool buf generate";};

        description = ''
          A command per GitHub repository, by `owner/name`, that prepares a
          fresh checkout of it, such as generating files the repository does
          not commit. `pr-loop worktrees` runs it with `sh` in each worktree
          whose HEAD moved since its last setup, and `pr-loop setup [PATH]`
          in any worktree. It runs where `pr-loop` was called, inside a
          sandboxed session's sandbox; through the relay, only the lookup
          happens on this machine.

          Setup comes only from here, never from the repository: a repository
          that could name it would make the loop run its code.
        '';
      };

      allowedDomains = mkOption {
        type = types.listOf types.str;

        description = ''
          Hosts a sandboxed loop session's commands may reach, for fetching
          dependencies when building and testing. GitHub must not be among
          them: the relay is the loop's only way there.
        '';

        default = [
          "proxy.golang.org"
          "sum.golang.org"
          "storage.googleapis.com"
          "registry.npmjs.org"
          "pypi.org"
          "files.pythonhosted.org"
          "crates.io"
          "index.crates.io"
          "static.crates.io"
        ];
      };

      redCheck = mkOption {
        type = types.attrsOf (types.submodule {
          options = {
            test = mkOption {
              type = types.str;
              description = "The test command, run with `sh` at the top of a checkout.";
            };

            build = mkOption {
              type = types.nullOr types.str;
              default = null;

              description = ''
                A command that builds the tests without running them, which
                tells a test that fails to build without a change from one
                that fails as asserted. Without it, no commit's check
                exits 0.
              '';
            };

            testFiles = mkOption {
              type = types.listOf types.str;
              description = "Git pathspec globs (`:(glob)` magic) that match the test files.";
            };
          };
        });

        default = {};
        example = {
          "owner/name" = {
            test = "go test ./...";
            build = "go vet ./...";
            testFiles = ["**/*_test.go"];
          };
        };

        description = ''
          How `git red-check` tests each GitHub repository, by `owner/name`,
          where its flags do not say. It runs these wherever it is called,
          inside a sandboxed session's sandbox.

          They come only from here, never from the repository: a repository
          that could name them would make agents run its commands.
        '';
      };

      deniedReads = mkOption {
        type = types.listOf types.str;
        readOnly = true;

        description = ''
          Absolute paths a sandboxed loop session's commands may not read:
          credentials, and where agent sockets live. Every sandboxing
          launcher takes its list from here.
        '';

        default = let
          home = config.home.homeDirectory;
          proxy = config.wgn.home.mcp-proxy;
        in
          ["${home}/.config/gh" "${home}/.gnupg" "${home}/.ssh"]
          # gh keeps its token in the login keychain, which a sandboxed
          # command can otherwise read.
          ++ optional pkgs.stdenv.hostPlatform.isDarwin "${home}/Library/Keychains"
          ++ optional pkgs.stdenv.hostPlatform.isLinux "/run/user"
          ++ optionals proxy.enable [proxy.stateDir proxy.oauthDir];
      };

      protectedPaths = mkOption {
        type = types.listOf types.str;
        readOnly = true;

        description = ''
          Paths in the workspace that programs outside the sandbox run or
          load, which a loop session may read but not change: git hooks and
          config, which the next git command run outside would obey, and
          agents' project settings, which can name hooks that run outside any
          sandbox. Every sandboxing launcher takes its list from here.
        '';

        default = [".git/hooks" ".git/config" ".claude" ".codex" ".eca"];
      };

      cacheDir = mkOption {
        type = types.str;
        readOnly = true;
        default = "${config.xdg.cacheHome}/pr-loop";

        description = ''
          Build caches for sandboxed loop sessions, which may write here and
          nowhere that builds outside the sandbox read: a cache entry written
          by an agent would otherwise be compiled into a later build on the
          host.
        '';
      };

      cacheEnv = mkOption {
        type = types.attrsOf types.str;
        readOnly = true;

        description = ''
          Environment pointing the build caches at {option}`cacheDir`.
        '';

        default = {
          GOCACHE = "${cfg.prLoop.cacheDir}/go-build";
          GOMODCACHE = "${cfg.prLoop.cacheDir}/go-mod";
        };
      };

      sandboxCaches = mkOption {
        type = types.lines;
        readOnly = true;

        description = ''
          Shell lines a loop launcher runs before starting its sandboxed
          session, exporting {option}`cacheEnv` and creating its directories.
        '';

        default =
          concatStrings (mapAttrsToList (name: value: ''
              export ${name}=${escapeShellArg value}
            '')
            cfg.prLoop.cacheEnv)
          + ''
            mkdir -p ${escapeShellArgs (attrValues cfg.prLoop.cacheEnv)}
          '';
      };

      client = mkOption {
        type = types.package;
        readOnly = true;

        description = ''
          A static Linux build of `pr-loop` for this machine's architecture,
          in `bin`, for a sandbox guest to run as a client of
          `sandbox-relay serve`.
        '';

        default = pkgs.agent-tools.linuxClient;
      };
    };

    skills = mkOption {
      type = with types;
        attrsOf (
          coercedTo (either path lines)
          (content:
            if isPath content
            then {source = content;}
            else {text = content;})
          (submodule {
            options = {
              source = mkOption {
                type = nullOr path;
                default = null;

                description = ''
                  File linked in as the skill's SKILL.md, or the skill's
                  whole directory when the skill keeps scripts or Emacs Lisp
                  beside it. Mutually exclusive with {option}`text`.
                '';
              };

              text = mkOption {
                type = nullOr lines;
                default = null;

                description = ''
                  Skill written verbatim. Mutually exclusive with
                  {option}`source`.
                '';
              };

              sandbox = mkOption {
                type = bool;
                default = false;

                description = ''
                  Whether the skill works with nothing but the workspace and
                  the sandbox's own tools.

                  Off by default, because most of these drive something on
                  this machine -- an editor, a notifier, a command installed
                  here -- and none of it exists in the guest. Such a skill is
                  worse there than missing: it is offered to the agent, which
                  spends a turn calling it and gets nothing back.
                '';
              };
            };
          })
        );

      description = ''
        Skills offered to every enabled coding agent. A path is linked into
        the agent's skill directory; a string is written there verbatim.

        Give {option}`sandbox` instead of a bare path or string to say the
        skill may also be carried into a sandboxed session.
      '';

      default =
        {
          notify = ./skills/notify/SKILL.md;

        }
        # Whole directories: most of these load Emacs Lisp or run a script
        # that sits beside their SKILL.md. d2 and github-activity run only
        # when invoked by name.
        // genAttrs [
          "d2"
          "describe"
          "dired"
          "emacsclient"
          "file-links"
          "github-activity"
          "highlight"
          "open"
          "select"
        ] (name: {source = "${emacs-skills}/skills/${name}";})
        // optionalAttrs pkgs.stdenv.hostPlatform.isDarwin {
          trash = ./skills/trash/SKILL.md;
        }
        // {
          signing-pass = {
            source = ./skills/signing-pass/SKILL.md;
            sandbox = true;
          };
          restack-branches = {
            source = ./skills/restack-branches/SKILL.md;
            sandbox = true;
          };
        }
        // optionalAttrs cfg.prLoop.enable {
          review-loop = {
            source = ./skills/review-loop/SKILL.md;
            sandbox = true;
          };
        };
    };
  };

  config = mkIf cfg.enable {
    # Read by `pr-loop` where it runs (a client with a relay asks the relay,
    # which reads it on the host), and by `git red-check` wherever it runs,
    # so a sandbox that runs it must let it read this.
    xdg.configFile."pr-loop/config.json" = mkIf cfg.prLoop.enable {
      text = builtins.toJSON {inherit (cfg.prLoop) bots setup redCheck;};
    };

    home.packages = with pkgs;
      [
        # For the d2 and github-activity skills; the latter fetches
        # matplotlib through uv when it runs.
        d2Png
        uv
      ]
      # Every agent is told to leave signing to `git sign-pass`, which this
      # installs. These run the git and gh on PATH, so the user's signing and
      # SSH settings apply.
      ++ [agent-tools];
  };
}
