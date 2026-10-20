{
  computerName,
  config,
  emacs-skills,
  lib,
  mattpocock-skills,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.llm;

  agentToolsConfig = pkgs.writeText "agent-tools-config.json" (builtins.toJSON {
    inherit (cfg) repos;
    plans = {
      inherit (cfg.plans) dir host;
      inherit (cfg.plans.repository) remote;
      repo = cfg.plans.repository.path;
      sshConfig = "${config.xdg.configHome}/agent-tools/notes-ssh-config";
      key = notesKey;
    };
    guard = {
      inherit (cfg.guard) credentialPaths;
    };
  });

  agentPlan = getExe' cfg.agentTools "agent-plan";

  # The notes repository's deploy key, which only git's ssh reads: every
  # agent is denied it, by the guard and by Claude Code's Read rules for
  # ~/.ssh/id_*, and loop sandboxes cannot read ~/.ssh at all.
  notesKey = "${config.home.homeDirectory}/.ssh/id_notes_deploy";

  # sops-nix fails every secret when one it is told of is missing from the
  # file, so the key is declared only once the file holds it. The file's
  # top-level keys are not encrypted.
  hasNotesKey = hasInfix "\nnotes-deploy-key:" ("\n" + builtins.readFile config.sops.defaultSopsFile);

  # Its own config, through `ssh -F`, so that nothing in ~/.ssh/config, a
  # shared master or an agent's key among them, applies, and a missing key
  # fails rather than prompts.
  notesSshConfig = ''
    Host github-notes
        HostName github.com
        User git
        IdentityFile ${notesKey}
        IdentitiesOnly yes
        IdentityAgent none
        ForwardAgent no
        ControlMaster no
        ControlPath none
        BatchMode yes
        ConnectTimeout 10
        StrictHostKeyChecking yes
        UserKnownHostsFile ${pkgs.writeText "github-known-hosts" ''
      github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
    ''}
  '';

  # Where the sync finds git, and ssh: on macOS, the system's.
  syncPath =
    if pkgs.stdenv.hostPlatform.isDarwin
    then "${pkgs.git}/bin:/usr/bin:/bin"
    else "${pkgs.git}/bin:${pkgs.openssh}/bin:/usr/bin:/bin";

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

    repos = mkOption {
      type = types.attrsOf (types.submodule {
        options = {
          setup = mkOption {
            type = types.nullOr types.str;
            default = null;

            description = ''
              A command that prepares a fresh checkout, such as generating
              files the repository does not commit. `pr-loop worktrees` runs
              it with `sh` in each worktree whose HEAD moved since its last
              setup, and `pr-loop setup [PATH]` in any worktree. It runs
              where `pr-loop` was called, inside a sandboxed session's
              sandbox; through the relay, only the lookup happens on this
              machine.
            '';
          };

          test = mkOption {
            type = types.nullOr types.str;
            default = null;

            description = ''
              The test command `git red-check` runs, with `sh` at the top of
              a checkout. `git fresh-review` never runs it, since it runs
              the reviewed commits' code; the session does.
            '';
          };

          build = mkOption {
            type = types.nullOr types.str;
            default = null;

            description = ''
              A command that builds the tests without running them, which
              tells `git red-check` a test that fails to build without a
              change from one that fails as asserted. Without it, no
              commit's check exits 0.
            '';
          };

          testFiles = mkOption {
            type = types.listOf types.str;
            default = [];
            description = "Git pathspec globs (`:(glob)` magic) that match the test files.";
          };

          routes = mkOption {
            type = types.listOf (types.submodule {
              options = {
                route = mkOption {
                  # The routes a focused review exists for: agent-tools'
                  # routes.Known, which refuses any other at lookup.
                  type = types.enum ["security"];
                  description = "The focused review the rule routes to.";
                };

                paths = mkOption {
                  type = types.listOf types.str;
                  default = [];

                  description = ''
                    Globs over paths from the top of the repository: `**/`
                    is any run of directories, `/**` at the end anything
                    below, `*` and `?` stay within one directory.
                  '';
                };

                lines = mkOption {
                  type = types.listOf types.str;
                  default = [];

                  description = ''
                    RE2 expressions matched against each line a commit adds
                    or removes, in the files `paths` matches where it names
                    any.
                  '';
                };

                why = mkOption {
                  type = types.str;
                  description = "Why a change it matches needs the review, which the routing reports.";
                };
              };
            });

            default = [];

            description = ''
              Rules that route this repository's commits to focused reviews,
              beside the built-in ones, which they only add to. A rule that
              names no paths and no lines, or does not compile, makes every
              lookup of these settings refuse.
            '';
          };

          threatNotes = mkOption {
            type = types.nullOr (types.either types.path types.lines);
            default = null;

            description = ''
              The threat notes `git fresh-review --focus security` gives
              its reviewer: what must hold (IDs such as `T3`), the untrusted
              inputs and boundaries, and what is accepted and why. The
              notes themselves, or a file outside the repository, named by
              an absolute path without whitespace (anything else is read as
              the notes): a file the repository holds would let a change
              write its own, and makes every lookup of these settings
              refuse. Without them the security checklist alone applies.
            '';
          };
        };
      });

      default = {};
      example = {
        "owner/name" = {
          setup = "go tool buf generate";
          test = "go test ./...";
          build = "go vet ./...";
          testFiles = ["**/*_test.go"];
        };
      };

      description = ''
        How agents' tools prepare, build and test each GitHub repository, by
        `owner/name`, written to `agent-tools/config.json` under the XDG
        config directory. The commands run wherever the tool is called,
        inside a sandboxed session's sandbox, so a sandbox that runs them
        must let them read that file.

        They come only from here, never from the repository: a repository
        that could name them would make agents run its commands.
      '';
    };

    plans = {
      dir = mkOption {
        type = types.str;
        default = "${config.home.homeDirectory}/git/notes/agents";

        description = ''
          Where `agent-plan` keeps every host's plans, side by side, each
          file named for its host. Org agenda files and refile targets must
          not include it.
        '';
      };

      host = mkOption {
        type = types.str;
        default = computerName;

        description = ''
          This machine's name in its plans' file names. It holds no dot,
          which separates the parts of those names.
        '';
      };

      repository = {
        path = mkOption {
          type = types.str;
          default = dirOf cfg.plans.dir;

          description = ''
            The git repository {option}`dir` is in, which activation clones
            when it is missing and the watcher syncs whenever a plan
            changes: a launchd agent on macOS, a systemd path unit on Linux.
          '';
        };

        remote = mkOption {
          type = types.str;
          default = "git@github-notes:wesnel/notes.git";

          description = ''
            The repository's origin. `github-notes` is a host of the ssh
            config the sync alone uses, with the `notes-deploy-key` secret
            as its only key.
          '';
        };

        key = mkOption {
          type = types.str;
          readOnly = true;
          default = notesKey;

          description = ''
            Where the deploy key is installed, once secrets/wgn.yaml holds
            `notes-deploy-key`. Every sandbox that can reach it denies it.
          '';
        };
      };
    };

    guard.credentialPaths = mkOption {
      type = types.listOf types.str;

      description = ''
        Absolute path prefixes of credentials, which stay out of every
        session: the guard denies a shell command or file tool naming a path
        that starts with one, in Claude Code and Codex alike; Claude Code's
        `Read` is denied them through `permissions.deny`; and the host
        sandbox denies its commands reading them. A prefix matches a file or
        directory of any name it starts, as `~/.ssh/id_` does every private
        key, and one ending in `/` only that directory and what it holds.
        The guard matches one holding a glob against a path and each of its
        parents. The sandbox takes each as a whole path, so a prefix that
        names no file, as `~/.ssh/id_`, must lie in a directory
        {option}`wgn.home.llm.sandbox.deniedReads` hides whole.
      '';

      default = let
        home = config.home.homeDirectory;
        proxy = config.wgn.home.mcp-proxy;
      in
        [
          "${home}/.ssh/id_"
          "${home}/.gnupg/private-keys"
          "${home}/.config/gh/"
          # Agents' logins: Codex's, and Claude Code's on Linux.
          "${home}/.codex/auth.json"
          "${home}/.claude/.credentials.json"
          proxy.tokenFile
          proxy.oauthDir
          "${home}/.config/sops/"
          "${home}/.config/sops-nix/key.txt"
          "${home}/.config/ngrok"
          "${home}/.config/op/"
          "${home}/.lima/_config"
          "${home}/.aws/"
          "${home}/.docker/config.json"
          "${home}/.netrc"
          "${home}/.authinfo"
          "${home}/.authinfo.gpg"
          "${home}/.npmrc"
          "${home}/.cargo/credentials"
          "${home}/.cargo/credentials.toml"
        ]
        # sops-nix decrypts into the per-user temporary directory on macOS
        # and /run/user on Linux; ~/.config/sops-nix/secrets links there.
        # The sandbox matches the path a link resolves to, and denying the
        # link's own directory would hide the file git needs below as well.
        ++ optionals pkgs.stdenv.hostPlatform.isDarwin [
          "${home}/Library/Application Support/ngrok"
          "/private/var/folders/*/*/T/secrets.d"
        ]
        ++ optional pkgs.stdenv.hostPlatform.isLinux "/run/user/*/secrets.d";
    };

    agentTools = mkOption {
      type = types.package;
      readOnly = true;

      description = ''
        agent-tools built with `guard.credentialPaths`, which its guard
        falls back on where its config is absent, as in a sandbox guest,
        whose home differs: a path under home is kept as `~/…`; and with
        the XDG config and state directories, which it then takes from the
        build rather than from a command's environment. Use it, not
        `pkgs.agent-tools`, for every program agents or hooks run.
      '';

      default = let
        home = config.home.homeDirectory;
        homeRelative = p:
          if hasPrefix "${home}/" p
          then "~/${removePrefix "${home}/" p}"
          else p;
      in
        pkgs.agent-tools.override {
          credentialPaths = map homeRelative cfg.guard.credentialPaths;
          configHome = config.xdg.configHome;
          stateHome = config.xdg.stateHome;
        };
    };

    # Every sandbox takes these lists from here: the host sandbox around
    # every Claude Code session's commands, and each loop session's.
    sandbox = {
      deniedReads = mkOption {
        type = types.listOf types.str;
        readOnly = true;

        description = ''
          Absolute paths no sandboxed command may read: credentials, and
          where agent sockets live.
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

      allowedDomains = mkOption {
        type = types.listOf types.str;
        readOnly = true;

        description = ''
          Hosts every sandbox's commands may reach, for fetching
          dependencies when building and testing. GitHub must not be among
          them: a loop session's only way there is the relay, and the host
          sandbox names it in a list of its own.
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

      allowedReads = mkOption {
        type = types.listOf types.str;
        readOnly = true;

        description = ''
          The only paths under the home directory a sandboxed loop session's
          commands may read; the rest of it is denied. `{workspace}` and
          `{gitdir}` are filled in by `sandbox-relay session`: the workspace,
          and the git directory of a linked worktree, which lies outside it.
          A read allowed here wins over a denied one, so no entry may contain
          a path in {option}`wgn.home.llm.sandbox.deniedReads`. Not `~/.config/git`: git stops
          when its config includes a file it cannot read, so sessions get
          {option}`gitConfig` instead.
        '';

        default = [
          "{workspace}"
          "{gitdir}"
          cfg.prLoop.cacheDir
          "${config.xdg.configHome}/agent-tools"
        ];
      };

      gitConfig = mkOption {
        type = types.path;
        readOnly = true;

        description = ''
          The global git config of a sandboxed loop session's commands, in
          place of the user's, which includes secrets. It holds no secret and
          includes nothing; the launcher passes the commit identity in the
          environment.
        '';

        default = pkgs.writeText "pr-loop-gitconfig" ''
          [commit]
          	gpgsign = false
          [tag]
          	gpgsign = false
          [init]
          	defaultBranch = main
        '';
      };

      gitIdentity = mkOption {
        type = types.lines;
        readOnly = true;

        description = ''
          Shell lines a loop launcher runs, outside the sandbox, to export the
          user's commit identity from their own git config for the session's
          commands, which read {option}`gitConfig` instead.
        '';

        default = ''
          for role in AUTHOR COMMITTER; do
            export "GIT_''${role}_NAME=$(${getExe pkgs.git} config --global --get user.name || true)"
            export "GIT_''${role}_EMAIL=$(${getExe pkgs.git} config --global --get user.email || true)"
          done
        '';
      };

      reviewDir = mkOption {
        type = types.str;
        readOnly = true;
        default = "${config.xdg.cacheHome}/pr-review";

        description = ''
          Where `pr-loop review` keeps each pull request's clone and review,
          `<owner>/<name>/<number>/`. A loop session started in a review's
          directory has that directory as its workspace, so it reads only that
          review.
        '';
      };

      reviewSession = mkOption {
        type = types.lines;
        readOnly = true;

        description = ''
          Shell lines a loop launcher runs before choosing its settings:
          `review` is set when the session starts in a head's review
          directory, {option}`reviewDir`/OWNER/NAME/N/HEAD12, and the launcher
          exits anywhere else under {option}`reviewDir`, where the session's
          workspace would hold the record and clone that `pr-loop review`
          trusts.
        '';

        default = ''
          review=
          here="$(pwd -P)"
          reviews="$(cd ${escapeShellArg cfg.prLoop.reviewDir} 2>/dev/null && pwd -P || printf %s ${escapeShellArg cfg.prLoop.reviewDir})"
          case "$here/" in
            "$reviews"/*)
              if [[ ! "''${here#"$reviews"/}" =~ ^[^/]+/[^/]+/[1-9][0-9]*/[0-9a-f]{12}$ ]]; then
                printf 'Start a loop session under %s only in a review directory, %s/OWNER/NAME/N/HEAD12.\n' "$reviews" "$reviews" >&2
                exit 2
              fi
              review=1
              ;;
          esac
        '';
      };

      reviewAllowedDomains = mkOption {
        type = types.listOf types.str;
        readOnly = true;
        default = remove "storage.googleapis.com" cfg.sandbox.allowedDomains;

        description = ''
          {option}`wgn.home.llm.sandbox.allowedDomains` for a session started under
          {option}`reviewDir`. storage.googleapis.com is left out: any
          bucket there can take data, and a review fetches its modules
          outside the session.
        '';
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

        default = [".git/hooks" ".git/config" ".claude" ".codex"];
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

      sandboxCaches = mkOption {
        type = types.lines;
        readOnly = true;

        description = ''
          Shell lines a loop launcher runs before starting its sandboxed
          session, pointing the build caches at {option}`cacheDir`.
        '';

        default = ''
          export GOCACHE=${escapeShellArg "${cfg.prLoop.cacheDir}/go-build"}
          export GOMODCACHE=${escapeShellArg "${cfg.prLoop.cacheDir}/go-mod"}
          mkdir -p "$GOCACHE" "$GOMODCACHE"
        '';
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
            };
          })
        );

      description = ''
        Skills offered to every enabled coding agent. A path is linked into
        the agent's skill directory; a string is written there verbatim.
      '';

      default =
        # Whole directories: most of these load Emacs Lisp or run a script
        # that sits beside their SKILL.md. d2 and github-activity run only
        # when invoked by name.
        genAttrs [
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
          signing-pass = ./skills/signing-pass/SKILL.md;
          write-tests = ./skills/write-tests/SKILL.md;
          pull-requests = ./skills/pull-requests/SKILL.md;
          docs-and-comments = ./skills/docs-and-comments/SKILL.md;
          design = ./skills/design/SKILL.md;
          restack-branches = ./skills/restack-branches/SKILL.md;
          review-change = ./skills/review-change/SKILL.md;
          plan-work = ./skills/plan-work/SKILL.md;
          coordinate-subagents = ./skills/coordinate-subagents/SKILL.md;
          diagnose-failure = ./skills/diagnose-failure/SKILL.md;
          retro = ./skills/retro/SKILL.md;
          # A directory for agents/openai.yaml, which keeps Codex from
          # invoking it unasked, as disable-model-invocation does in Claude
          # Code.
          grill = ./skills/grill;

          # Linked as published, with the repository's MIT licence beside
          # it: the licence requires its notice in every copy.
          writing-for-agents.source = pkgs.runCommandLocal "writing-for-agents" {} ''
            cp -R ${mattpocock-skills}/skills/productivity/writing-for-agents $out
            chmod u+w $out
            cp ${mattpocock-skills}/LICENSE $out/LICENSE
          '';
        }
        // optionalAttrs cfg.prLoop.enable {
          review-loop = ./skills/review-loop/SKILL.md;
        };
    };
  };

  config = mkIf cfg.enable {
    # Read by `pr-loop` where it runs; a client with a relay asks the relay,
    # which reads it on the host.
    xdg.configFile."pr-loop/config.json" = mkIf cfg.prLoop.enable {
      text = builtins.toJSON {inherit (cfg.prLoop) bots;};
    };

    # Read by `pr-loop setup` and `worktrees` as pr-loop's config is, by
    # `git red-check` wherever it runs, and by `agent-plan` and the guard,
    # which keeps each plan's log append-only.
    xdg.configFile."agent-tools/config.json".source = agentToolsConfig;

    xdg.configFile."agent-tools/notes-ssh-config".text = notesSshConfig;

    sops.secrets = mkIf hasNotesKey {
      notes-deploy-key.path = notesKey;
    };

    # A sandboxed session may write the plans directory and nothing around
    # it, and Linux's sandbox skips a grant whose path does not exist, so
    # the repository is cloned and the directory made here, outside any
    # sandbox. Neither step fails the switch: init says in one line why it
    # did not clone, and the sync's failure is a warning. Each is bounded
    # by agent-plan's own timeouts. agent-plan reads its config where the
    # build bakes it in, under xdg.configHome, which is this generation's
    # once linkGeneration has run.
    home.activation.agentPlans = hm.dag.entryAfter ["linkGeneration"] ''
      run --quiet env PATH=${syncPath}:"$PATH" ${agentPlan} init \
        || warnEcho "agent-plan init failed, so sandboxed sessions may not be able to make plans."
      run --quiet env PATH=${syncPath}:"$PATH" ${agentPlan} sync \
        || warnEcho "agent-plan sync failed; the watcher tries again on the next change to a plan."
    '';

    # Syncs plans a few seconds after one changes, outside every session.
    # launchd watches the directory's entries only, so agent-plan touches
    # the directory after each write it makes; a plan file edited in place
    # by anything else is sent with the next change.
    launchd.agents.agent-plan-sync = mkIf pkgs.stdenv.hostPlatform.isDarwin {
      enable = true;
      config = {
        ProgramArguments = [agentPlan "sync" "--settle" "5s"];
        WatchPaths = [cfg.plans.dir];
        ProcessType = "Background";
        EnvironmentVariables.PATH = syncPath;
        StandardOutPath = "/tmp/agent-plan-sync.log";
        StandardErrorPath = "/tmp/agent-plan-sync.log";
      };
    };

    systemd.user = mkIf pkgs.stdenv.hostPlatform.isLinux {
      paths.agent-plan-sync = {
        Unit.Description = "Sync agents' plans when one changes";
        Path.PathChanged = cfg.plans.dir;
        Install.WantedBy = ["default.target"];
      };

      services.agent-plan-sync = {
        Unit.Description = "Sync agents' plans with the notes repository";
        Service = {
          Type = "oneshot";
          Environment = ["PATH=${syncPath}"];
          ExecStart = "${agentPlan} sync --settle 5s";
        };
      };
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
      ++ [cfg.agentTools];
  };
}
