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
      inherit (cfg.plans) dir host peers;
      inherit (cfg.plans.repository) remote;
      repo = cfg.plans.repository.path;
      sshConfig = "${config.xdg.configHome}/agent-tools/notes-ssh-config";
      key = notesKey;
    };
    guard = {
      inherit (cfg.guard) webSockets credentialPaths;
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

  # Where the sync finds git, and ssh: on macOS, the system's, which the
  # interactive master to a peer was opened with.
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
              a checkout. `git fresh-review` and `pr-loop review` run it, and
              `git red-check` with it, only in the review VM, since it runs
              the reviewed commits' code.
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

          generated = mkOption {
            type = types.listOf types.str;
            default = [];

            description = ''
              Git pathspec globs (`:(glob)` magic) for files git ignores that
              a build needs and a fresh checkout lacks, such as what
              {option}`setup` generates. `pr-loop autosquash --exec` copies
              them into its scratch worktree before running its command.
            '';
          };

          testFiles = mkOption {
            type = types.listOf types.str;
            default = [];
            description = "Git pathspec globs (`:(glob)` magic) that match the test files.";
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
          generated = ["**/*.pb.go"];
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

      peers = mkOption {
        type = types.listOf (types.submodule {
          options = {
            ssh = mkOption {
              type = types.str;
              description = "The peer as ssh names it.";
            };
            command = mkOption {
              type = types.str;
              default = "agent-plan";
              description = "`agent-plan` on the peer.";
            };
          };
        });
        default = [];

        description = ''
          Hosts told to pull whenever this host's sync pushes, only over an
          ssh master connection to them that already exists, since a new
          connection could wait for a key touch.
        '';
      };
    };

    guard.webSockets = mkOption {
      type = types.listOf types.str;
      default = [];
      example = ["wss://events.example.com/"];

      description = ''
        URL prefixes a Claude Code Monitor may open a WebSocket to; the guard
        denies any other. Claude Code opens it itself, outside the sandbox.
        End each with the `/` after the host, or a lookalike host matches
        too.
      '';
    };

    guard.credentialPaths = mkOption {
      type = types.listOf types.str;

      description = ''
        Absolute path prefixes of credentials, which stay out of every
        session: the guard denies a shell command or file tool naming a path
        that starts with one, and Claude Code's `Read` is denied them through
        `permissions.deny`. A prefix matches a file or directory of any name
        it starts, as `~/.ssh/id_` does every private key.
      '';

      default = let
        home = config.home.homeDirectory;
        proxy = config.wgn.home.mcp-proxy;
      in [
        "${home}/.ssh/id_"
        "${home}/.gnupg/private-keys"
        "${home}/.codex/auth.json"
        "${home}/.claude/.credentials.json"
        proxy.tokenFile
        proxy.oauthDir
        "${home}/.config/sops/age"
        "${home}/.config/ngrok"
      ];
    };

    agentTools = mkOption {
      type = types.package;
      readOnly = true;

      description = ''
        agent-tools built with `guard.credentialPaths`, which its guard
        falls back on where its config is absent, as in a sandbox guest,
        whose home differs: a path under home is kept as `~/…`. Use it, not
        `pkgs.agent-tools`, for every program agents or hooks run.
      '';

      default = let
        home = config.home.homeDirectory;
        homeRelative = p:
          if hasPrefix "${home}/" p
          then "~/${removePrefix "${home}/" p}"
          else p;
      in
        pkgs.agent-tools.override {credentialPaths = map homeRelative cfg.guard.credentialPaths;};
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

      allowedReads = mkOption {
        type = types.listOf types.str;
        readOnly = true;

        description = ''
          The only paths under the home directory a sandboxed loop session's
          commands may read; the rest of it is denied. `{workspace}` and
          `{gitdir}` are filled in by `sandbox-relay session`: the workspace,
          and the git directory of a linked worktree, which lies outside it.
          A read allowed here wins over a denied one, so no entry may contain
          a path in {option}`deniedReads`. Not `~/.config/git`: git stops
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
          review. Emacs's `wgn/untrusted-directories` holds this path too.
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
        default = remove "storage.googleapis.com" cfg.prLoop.allowedDomains;

        description = ''
          {option}`allowedDomains` for a session started under
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
          sandbox; and the loop session's logs, from which `pr-loop watch
          --events` tells the session's starter of proposals and the
          hand-back. Every sandboxing launcher takes its list from here.
        '';

        default = [".git/hooks" ".git/config" ".claude" ".codex" ".eca" ".git/pr-loop-session.jsonl" ".git/pr-loop-session.err"];
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

      handBack = mkOption {
        type = types.lines;
        readOnly = true;

        description = ''
          Shell lines defining `pr_loop_run LAUNCHER COMMAND...`, which a
          loop launcher runs its session with, outside the sandbox: it runs
          COMMAND, passes TERM, HUP and INT (as TERM, since a command run in
          the background ignores INT) on to it, and once it has exited,
          unless `review` is set, runs `pr_loop_hand_back`, then exits with
          COMMAND's status. The launcher is not COMMAND's process, so
          `pr-loop stop`, which stops the session and what runs under it,
          leaves the hand-back to run after it.

          `pr_loop_hand_back LAUNCHER STATUS STARTED` waits up to a minute
          for a `report.md` written since STARTED (seconds since the epoch),
          as `pr-loop stop` writes one once the session has exited, then
          notifies the operator, through Emacs's `wgn/notify` or else, on
          macOS, Notification Center, naming the report if there is one,
          and prints the same on stderr after `LAUNCHER: the loop session
          exited`, the line `pr-loop watch --events handback` waits for. A
          sandboxed session may reach neither notifier.
        '';

        default = ''
          pr_loop_run() {
            local launcher=$1 started child status=0
            shift
            started=$(date +%s)
            # Without job control a command run with & would read /dev/null.
            "$@" <&0 &
            child=$!
            trap 'kill -TERM "$child" 2>/dev/null || true' TERM INT
            trap 'kill -HUP "$child" 2>/dev/null || true' HUP
            # wait returns early when a trapped signal arrives; the last
            # wait, once the command has gone, gives its status.
            while :; do
              wait "$child" && status=0 || status=$?
              kill -0 "$child" 2>/dev/null || break
            done
            trap - TERM INT HUP
            if [[ -z "''${review:-}" ]]; then
              pr_loop_hand_back "$launcher" "$status" "$started"
            fi
            exit "$status"
          }

          pr_loop_hand_back() {
            local launcher=$1 status=$2 started=$3 common report title message tries=0
            common="$(${getExe pkgs.git} rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
            report="$common/pr-loop/report.md"
            title="Review loop ended in $(basename "$(${getExe pkgs.git} rev-parse --show-toplevel 2>/dev/null || pwd)")"
            fresh() {
              [[ -n "$common" && -f "$report" ]] && (( $(${getExe' pkgs.coreutils "stat"} -c %Y "$report") >= started ))
            }
            while ! fresh && (( tries < 30 )); do
              sleep 2
              tries=$((tries + 1))
            done
            if fresh; then
              message="exit $status; the report is $report"
            else
              message="exit $status, with no report; read the session's log"
            fi
            printf '%s: the loop session exited: %s\n' "$launcher" "$message" >&2
            # Lisp strings end at a double quote and escape with a backslash.
            if ! { command -v emacsclient >/dev/null &&
              ${getExe' pkgs.coreutils "timeout"} 10 emacsclient -e "(wgn/notify \"''${title//[\"\\]/}\" \"''${message//[\"\\]/}\")" >/dev/null 2>&1; }; then
              if [[ -x /usr/bin/osascript ]]; then
                /usr/bin/osascript -e 'on run argv' -e 'display notification (item 2 of argv) with title (item 1 of argv)' \
                  -e 'end run' "$title" "$message" >/dev/null 2>&1 || true
              fi
            fi
          }
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

        default = cfg.agentTools.linuxClient;
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
          write-tests = {
            source = ./skills/write-tests/SKILL.md;
            sandbox = true;
          };
          pull-requests = {
            source = ./skills/pull-requests/SKILL.md;
            sandbox = true;
          };
          docs-and-comments = {
            source = ./skills/docs-and-comments/SKILL.md;
            sandbox = true;
          };
          design = {
            source = ./skills/design/SKILL.md;
            sandbox = true;
          };
          restack-branches = {
            source = ./skills/restack-branches/SKILL.md;
            sandbox = true;
          };
          review-change = {
            source = ./skills/review-change/SKILL.md;
            sandbox = true;
          };
          plan-work = {
            source = ./skills/plan-work/SKILL.md;
            sandbox = true;
          };
          coordinate-subagents = ./skills/coordinate-subagents/SKILL.md;
          diagnose-failure = {
            source = ./skills/diagnose-failure/SKILL.md;
            sandbox = true;
          };
          # Reads the session transcripts this machine keeps.
          retro = ./skills/retro/SKILL.md;
          # A directory for agents/openai.yaml, which keeps Codex from
          # invoking it unasked, as disable-model-invocation does in Claude
          # Code.
          grill = {
            source = ./skills/grill;
            sandbox = true;
          };

          # Linked as published, with the repository's MIT licence beside
          # it: the licence requires its notice in every copy.
          writing-for-agents = {
            source = pkgs.runCommandLocal "writing-for-agents" {} ''
              cp -R ${mattpocock-skills}/skills/productivity/writing-for-agents $out
              chmod u+w $out
              cp ${mattpocock-skills}/LICENSE $out/LICENSE
            '';
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
    # Read by `pr-loop` where it runs; a client with a relay asks the relay,
    # which reads it on the host.
    xdg.configFile."pr-loop/config.json" = mkIf cfg.prLoop.enable {
      text = builtins.toJSON {
        inherit (cfg.prLoop) bots;
        # Where `pr-loop review` runs a pull request's tests: the ECA
        # sandbox's backend, never the eca-sandbox wrapper, which brings
        # this machine's maps, secrets and state, and the guest's tools;
        # and where it keeps reviews, which it reads from here alone.
        review = {
          backend =
            if pkgs.stdenv.hostPlatform.isDarwin
            then getExe' pkgs.eca-gondolin "eca-gondolin"
            else getExe' pkgs.eca-bwrap "eca-bwrap";
          guestTools = "${cfg.prLoop.client}/bin";
          dir = cfg.prLoop.reviewDir;
        };
      };
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
    # by agent-plan's own timeouts.
    home.activation.agentPlans = hm.dag.entryAfter ["writeBoundary"] ''
      run --quiet env AGENT_TOOLS_CONFIG=${agentToolsConfig} PATH=${syncPath}:"$PATH" ${agentPlan} init \
        || warnEcho "agent-plan init failed, so sandboxed sessions may not be able to make plans."
      run --quiet env AGENT_TOOLS_CONFIG=${agentToolsConfig} PATH=${syncPath}:"$PATH" ${agentPlan} sync \
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
