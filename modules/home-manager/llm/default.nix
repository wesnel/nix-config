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
    plans = {
      inherit (cfg.plans) dir host;
      inherit (cfg.plans.repository) remote;
      repo = cfg.plans.repository.path;
      sshConfig = "${config.xdg.configHome}/${notesSshConfigFile}";
      key = notesKey;
      # The playbooks skill's, which a file of the same name in the
      # repository's templates overrides.
      templates = "${./skills/playbooks/templates}";
    };
    guard = {
      inherit (cfg.guard) credentialPaths excludedCommands;
    };
    delegate =
      {
        inherit (cfg.delegate) agents settings;
      }
      # The provider git delegate's Codex reviews reach their model
      # through, whose auth command stands in for Codex's login.
      // optionalAttrs ngrokAi {
        codex = {
          model = cfg.ngrokAi.models.review;
          inherit (cfg.ngrokAi) contextWindow provider;
        };
      };
    publicText = {
      inherit (cfg) harnessRepos;
    };
    review = {
      inherit (cfg.review) privateModules;
    };
    # The servers this host's mcp-proxy serves, by the names mcp.json keys
    # their services by: the guard names `mcp-proxy-authorize NAME` only for
    # these.
    mcpProxy =
      if config.wgn.home.mcp-proxy.enable
      then {inherit (config.wgn.home.mcp-proxy) url endpoints;}
      else null;
  });

  agentPlan = getExe' cfg.agentTools "agent-plan";

  # The notes repository's deploy key, which only git's ssh reads: every
  # agent is denied it, by the guard and by Claude Code's Read rules for
  # ~/.ssh/id_*, and the sandbox cannot read ~/.ssh at all.
  notesKey = "${config.home.homeDirectory}/.ssh/id_notes_deploy";

  # sops-nix fails every secret when one it is told of is missing from the
  # file, so the key is declared only once the file holds it. The file's
  # top-level keys are not encrypted.
  hasNotesKey = hasInfix "\nnotes-deploy-key:" ("\n" + builtins.readFile config.sops.defaultSopsFile);

  ngrokAi = cfg.gateway == "ngrok-ai";
  # A file of the personal machines' own: .sops.yaml leaves the work Mac
  # out of its recipients, which every other file under secrets/ has.
  ngrokAiSopsFile = ../../../secrets/personal.yaml;
  hasNgrokAiKey = hasInfix "\nngrok-ai-key:" ("\n" + builtins.readFile ngrokAiSopsFile);

  # Its own config, through `ssh -F`, so that nothing in ~/.ssh/config, a
  # shared master or an agent's key among them, applies, and a missing key
  # fails rather than prompts. ~/.ssh/config includes it too, so the
  # operator's own git in the clone reaches github-notes.
  notesSshConfigFile = "agent-tools/notes-ssh-config";
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

    gateway = mkOption {
      type = types.enum ["none" "ngrok-ai"];
      default = "none";

      description = ''
        Where every enabled coding agent reaches its models: `none`, through
        each agent's own login; `ngrok-ai`, through the ngrok.ai AI gateway
        on the models in {option}`wgn.home.llm.ngrokAi.models`, for Claude
        Code, its sub-agents and headless reviewers, Codex, and git
        delegate's Codex reviews. The gateway's access key is the sops
        secret `ngrok-ai-key` in secrets/personal.yaml, which this machine
        must be a recipient of; an assertion fails while the file lacks it.
        Each agent runs a command that reads the key, so it is in no
        settings file and no environment.

        Claude Code's and Codex's settings are merged into files they own,
        so setting this back to `none` leaves the gateway's keys there:
        remove `apiKeyHelper` and `env`'s `ANTHROPIC_*`,
        `CLAUDE_CODE_MAX_CONTEXT_TOKENS` and `DISABLE_TELEMETRY` from
        ~/.claude/settings.json, and `model_provider`, `model`,
        `review_model`, `model_context_window`, `[model_providers.ngrok-ai]`
        and `[features] memories` from ~/.codex/config.toml.
      '';
    };

    ngrokAi = {
      models = {
        primary = mkOption {
          type = types.str;
          default = "deepseek-v4-pro";
          description = "The ngrok.ai model behind Claude Code's `opus`, `sonnet` and `fable` aliases, and interactive Codex's model.";
        };

        fast = mkOption {
          type = types.str;
          default = "deepseek-v4-flash";
          description = "The ngrok.ai model behind Claude Code's `haiku` alias, which also runs its background work.";
        };

        review = mkOption {
          type = types.str;
          default = "xiaomi/mimo-v2.6-pro";
          description = ''
            The ngrok.ai model of the second opinion, of a model family other
            than {option}`primary`'s: git delegate's Codex review, Codex's
            `/review`, and the adversarial-reviewer sub-agent.
          '';
        };
      };

      contextWindow = mkOption {
        type = types.ints.between 100000 1000000;
        default = 200000;
        description = ''
          The context window, in tokens, Claude Code and Codex assume for the
          gateway's models, which they do not recognize: a session compacts
          as it nears it. The models take more; a smaller window spends less
          per request.
        '';
      };

      baseURL = mkOption {
        type = types.str;
        readOnly = true;
        internal = true;
        default = "https://gateway.ngrok.ai";
        description = "The gateway's address, under which `/v1` serves the Anthropic Messages and OpenAI Responses APIs.";
      };

      keyCommand = mkOption {
        type = types.listOf types.str;
        readOnly = true;
        internal = true;
        default = ["${pkgs.coreutils}/bin/cat" config.sops.secrets.ngrok-ai-key.path];
        defaultText = literalExpression ''["''${pkgs.coreutils}/bin/cat" config.sops.secrets.ngrok-ai-key.path]'';
        description = "The command, and its arguments, that prints the gateway's access key and nothing else.";
      };

      provider = mkOption {
        type = types.attrs;
        readOnly = true;
        internal = true;
        default = {
          id = "ngrok-ai";
          name = "ngrok.ai";
          baseURL = "${cfg.ngrokAi.baseURL}/v1";
          authCommand = head cfg.ngrokAi.keyCommand;
          authArgs = tail cfg.ngrokAi.keyCommand;
        };
        defaultText = literalExpression ''{ id = "ngrok-ai"; name = "ngrok.ai"; baseURL = "''${baseURL}/v1"; authCommand = head keyCommand; authArgs = tail keyCommand; }'';
        description = ''
          The gateway as an OpenAI Responses provider, for Codex: its id in
          `model_providers`, its name, its base URL, and the command, with
          its arguments, that prints the key as the bearer token. Codex's
          config.toml and git delegate's Codex runs both take it from here.
        '';
      };
    };

    harnessRepos = mkOption {
      type = types.listOf types.str;
      default = ["wesnel/nix-config" "wesnel/emacs-config"];

      description = ''
        The GitHub repositories, by `owner/name`, that the agents' harness
        itself lives in, matched against a checkout's remotes. Text bound for
        GitHub from one of them may name the harness's tools and state
        directories; from any other repository, it is checked in full.
      '';
    };

    plans = {
      dir = mkOption {
        type = types.str;
        default = "${cfg.plans.repository.path}/agents/plans";

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
          default = "${config.home.homeDirectory}/git/github.com/wesnel/notes";

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

    review.privateModules = mkOption {
      type = types.listOf types.str;
      default = ["github.com/ngrok/*" "github.com/ngrok-private/*" "go.ngrok.com/*"];

      description = ''
        Go module path patterns, as GOPRIVATE's, that the upgrade review
        (`git delegate --reviewer upgrade`) never asks the public module
        proxy for, beside those GOPRIVATE, GONOPROXY and GONOSUMDB name. It
        reads them from a local clone or through the gh login's GitHub API
        alone, and raises a pin it cannot read as unread. Setting GOPRIVATE
        for these instead would have go fetch them over SSH, which waits on
        the hardware key. Matched as github.com/OWNER/REPO, they also name
        the private GitHub repositories it may read, for the operator's own
        change alone (a range, or a pull request whose author is the gh
        login's user): any other repository, and for anyone else's pull
        request every repository, is read only where GitHub says it is
        public.
      '';
    };

    guard.excludedCommands = mkOption {
      type = types.listOf types.str;
      default = [];
      internal = true;

      description = ''
        The command patterns Claude Code's sandbox runs outside it, which
        the Claude Code module sets to its `sandbox.excludedCommands`. The
        guard refuses a Claude Code command line that names one anywhere
        but as the whole line, where it may run sandboxed and fail.
      '';
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
        # The secret's link, besides where it resolves to below.
        ++ optional ngrokAi config.sops.secrets.ngrok-ai-key.path
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

    # The headless reviewers' definitions and settings, which the Claude Code
    # module builds from its agent files and host sandbox, written into
    # agent-tools' config for git delegate.
    delegate = {
      agents = mkOption {
        type = types.nullOr types.str;
        default = null;
        internal = true;
        description = "The store path of the reviewers' `--agents` file, review-agents.json.";
      };

      settings = mkOption {
        type = types.nullOr types.str;
        default = null;
        internal = true;
        description = "The store path of the reviewers' only settings file, reviewer-settings.json.";
      };
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

    # The host sandbox around every Claude Code session's commands takes
    # these lists from here.
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
          dependencies when building and testing. The host sandbox names
          GitHub in a list of its own.
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
        the `pr-loop` command's config and the `review-loop` skill, through
        which a review loop pushes, replies and requests reviews on GitHub.
        It runs outside the sandbox with the `gh` login, so its policy
        guards an agent that forgets a rule but does not stop one that runs
        `gh` itself
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
          # emacs-skills' name, so the habit and trigger keep working, for
          # checks a sandboxed command can run: no daemon is reachable.
          emacsclient = ./skills/emacsclient/SKILL.md;
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
          # A directory, so the agent reading it can read the templates
          # plans.templates gives agent-plan.
          playbooks = ./skills/playbooks;
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
        }
        // import ./principles.nix;
    };
  };

  config = mkIf cfg.enable {
    # Read by `pr-loop` where it runs.
    xdg.configFile."pr-loop/config.json" = mkIf cfg.prLoop.enable {
      text = builtins.toJSON {inherit (cfg.prLoop) bots;};
    };

    # Read by `agent-plan`, `git delegate`, pr-loop's public-text check and
    # the guard, which keeps each plan's log append-only.
    xdg.configFile."agent-tools/config.json".source = agentToolsConfig;

    xdg.configFile.${notesSshConfigFile}.text = notesSshConfig;

    # Takes effect only where programs.ssh is enabled, as it writes
    # ~/.ssh/config.
    programs.ssh.includes = ["${config.xdg.configHome}/${notesSshConfigFile}"];

    sops.secrets = mkMerge [
      (mkIf hasNotesKey {
        notes-deploy-key.path = notesKey;
      })
      # Read by the agents' key command, wgn.home.llm.ngrokAi.keyCommand.
      (mkIf ngrokAi {
        ngrok-ai-key.sopsFile = ngrokAiSopsFile;
      })
    ];

    assertions = [
      {
        assertion = ngrokAi -> hasNgrokAiKey;
        message = "wgn.home.llm.gateway is ngrok-ai, but secrets/personal.yaml holds no ngrok-ai-key, which sops-nix would fail every secret for.";
      }
    ];

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
