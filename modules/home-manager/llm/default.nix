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
        inherit (cfg.delegate) agents settings quotaStop;
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
    agentPlans = {
      inherit (cfg.agentPlans) claudePage spacePage;
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

  # Initializes and syncs the plans, for activation and both watchers. Its
  # stderr is what the operator sees: the switch's output, launchd's log
  # file, the journal. Without the deploy key, which Darwin's secret
  # loading may not have written yet, it warns and leaves both to the
  # watcher's next run. A failed init still syncs, and then exits with
  # init's status.
  planSyncRunner = pkgs.writeShellScript "agent-plan-sync" ''
    set -euo pipefail
    if [[ -n ${escapeShellArg cfg.plans.repository.remote} && ! -r ${escapeShellArg notesKey} ]]; then
      echo "agent-plan-sync: plans are not synced until the deploy key is readable:" ${escapeShellArg notesKey} >&2
      exit 0
    fi
    status=0
    ${agentPlan} init || {
      status=$?
      echo "agent-plan-sync: agent-plan init failed (exit $status); syncing anyway" >&2
    }
    ${agentPlan} sync "$@" || exit
    exit "$status"
  '';

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
        remove from ~/.claude/settings.json the keys `gatewaySettings` in
        modules/home-manager/claude/default.nix sets, and from
        ~/.codex/config.toml those the `codexGateway` activation in
        modules/home-manager/codex/default.nix sets.
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

    # Read by plan-dash from agentPlans in agent-tools/config.json, which is
    # read-only and guarded, so no session can point publication elsewhere.
    agentPlans = {
      claudePage = mkOption {
        type = types.nullOr (types.strMatching "https://claude\\.ai/(code/)?artifact/[A-Za-z0-9-]{1,100}");
        default = null;

        description = ''
          The Agent plans page's claude.ai URL,
          `https://claude.ai/artifact/ID`, which the dashboard session
          ({option}`wgn.home.claude.agentPlans.enable`) keeps current.
          Unset, the session publishes a new page and gives its URL to set
          here.
        '';
      };

      spacePage = mkOption {
        type = types.nullOr (types.strMatching "[A-Za-z0-9_-]{1,256}");
        default = null;

        description = ''
          The ID of the ChatGPT Space page, such as `page_0123abcd`, that
          the dashboard session mirrors the Agent plans page to with
          `plan-dash space` after each update; unset, it mirrors nothing.
          The first run makes a Codex thread and a block at the page's end,
          and records both in `agent-plans/space.json` under
          {option}`xdg.stateHome`; later runs replace that block only. Set
          to another page, it makes a block there and leaves the old one.
          Needs Codex signed in to ChatGPT on the session's host, with the
          ChatGPT Space plugin connected.
        '';
      };
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
        {option}`wgn.home.llm.sandbox.deniedReads` hides whole, which then
        leaves the prefix out.
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

      quotaStop = mkOption {
        type = types.nullOr (types.ints.between 1 100);
        default = null;
        internal = true;
        description = ''
          The quota stop's threshold, {option}`wgn.home.claude.quotaStop`:
          the reviewers carry no hooks, so git delegate applies the stop
          itself before starting a Claude reviewer.
        '';
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

    # The sandboxes around Claude Code's and Codex's commands take these
    # lists from here.
    sandbox = {
      deniedReads = mkOption {
        type = types.listOf types.str;
        readOnly = true;

        description = ''
          Paths no sandboxed command may read: credentials, where agent
          sockets live, and what would let a command act as the operator
          elsewhere. Each is absolute or starts `~/`, and one holding a glob
          matches as Claude Code's sandbox matches it. A path in
          {option}`wgn.home.llm.sandbox.allowedReads` stays readable in
          Claude Code; Codex cannot carve a read out of a deny, so it leaves
          out a path holding one.
        '';

        default = let
          home = config.home.homeDirectory;
          proxy = config.wgn.home.mcp-proxy;
          # Hidden whole, with everything under them.
          directories =
            ["${home}/.config/gh" "${home}/.gnupg" "${home}/.ssh"]
            # gh keeps its token in the login keychain, which a sandboxed
            # command can otherwise read.
            ++ optional pkgs.stdenv.hostPlatform.isDarwin "${home}/Library/Keychains"
            ++ optional pkgs.stdenv.hostPlatform.isLinux "/run/user"
            ++ optionals proxy.enable [proxy.stateDir proxy.oauthDir];
          # The credential list, as the sandbox takes paths: no trailing
          # `/`, and none that is or is under a directory above, such as the
          # `~/.ssh/id_` prefix, which names no file.
          hidden = p: any (d: p == d || hasPrefix "${d}/" p) directories;
          credentials = filter (p: !hidden p) (map (removeSuffix "/") cfg.guard.credentialPaths);
        in
          directories
          ++ credentials
          # The guard refuses a .env file by its name; the sandbox needs
          # these patterns.
          ++ [
            "~/git/**/.env"
            "~/git/**/.env.*"
            "~/.claude/worktrees/**/.env"
            "~/.claude/worktrees/**/.env.*"
            # Browser profiles, 1Password and shell histories.
            "~/Library/Application Support/Google/Chrome"
            "~/Library/Application Support/BraveSoftware"
            "~/Library/Application Support/Firefox"
            "~/Library/Safari"
            "~/Library/Containers/com.apple.Safari"
            "~/.config/google-chrome"
            "~/.config/chromium"
            "~/.mozilla"
            "~/Library/Application Support/zen"
            "~/.zen"
            "~/.config/zen"
            "~/.var/app/app.zen_browser.zen"
            "~/Library/Application Support/1Password"
            "~/Library/Group Containers/2BUA8C4S2C.com.1password"
            "~/.config/1Password"
            "~/.local/share/fish/fish_history"
            "~/.zsh_history"
            "~/.zsh_sessions"
            "~/.bash_history"
            # Only the watcher's sync pushes plans.
            cfg.plans.repository.key
          ];
      };

      allowedReads = mkOption {
        type = types.listOf types.str;
        readOnly = true;

        description = ''
          Paths under {option}`wgn.home.llm.sandbox.deniedReads` that
          sandboxed commands may still read. Git reads the user's name and
          email through an include of the `git.inc` template on every run,
          and dies on an include it cannot read.
        '';

        default = optional (config.sops.templates ? "git.inc") (
          if pkgs.stdenv.hostPlatform.isDarwin
          then "/private/var/folders/*/*/T/secrets.d/*/rendered/git.inc"
          else "/run/user/*/secrets.d/*/rendered/git.inc"
        );
      };

      toolCaches = mkOption {
        type = types.listOf types.str;
        readOnly = true;

        description = ''
          The caches of Go, golangci-lint, Nix and uv where they are by
          default, which every sandbox lets its commands write. Nothing on
          PATH (~/go/bin among it) and nothing a program outside the
          sandbox loads code or configuration from belongs here.
        '';

        default =
          ["~/go/pkg"]
          ++ (
            if pkgs.stdenv.hostPlatform.isDarwin
            then ["~/Library/Caches/go-build" "~/Library/Caches/golangci-lint"]
            else ["~/.cache/go-build" "~/.cache/golangci-lint"]
          )
          ++ ["~/.cache/nix" "~/.cache/uv"];
      };

      deniedEnvVars = mkOption {
        type = types.listOf types.str;
        readOnly = true;

        description = ''
          Variables taken out of the environment sandboxed commands inherit
          from the agent. Only these are taken out, so every credential a
          module puts in the environment belongs here.
        '';

        default = [
          "GH_TOKEN"
          "GITHUB_TOKEN"
          "SSH_AUTH_SOCK"
          "KEYID"
          "NGROK_AUTHTOKEN"
          "NGROK_AUTHTOKEN_PROD"
          "NGROK_AI_KEY"
          "MCP_PROXY_TOKEN"
          "OPENAI_API_KEY"
          "ANTHROPIC_API_KEY"
          "CLAUDE_CODE_MESSAGING_TOKEN"
        ];
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
          questions = ./skills/questions/SKILL.md;
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
    # sandbox. Neither step fails the switch: a failure is a warning, and
    # the watcher retries initialization and sync. Each is bounded
    # by agent-plan's own timeouts. agent-plan reads its config where the
    # build bakes it in, under xdg.configHome, which is this generation's
    # once linkGeneration has run. Darwin starts secret loading
    # asynchronously, so a missing key defers both steps to the watcher.
    home.activation.agentPlans = hm.dag.entryAfter (["linkGeneration"] ++ optional (config.sops.secrets != {}) "sops-nix") ''
      run --quiet env PATH=${syncPath}:"$PATH" ${planSyncRunner} \
        || warnEcho "agent-plan initialization or sync failed; the watcher tries again within 5 minutes."
    '';

    # Syncs plans a few seconds after one changes, outside every session,
    # and every 5 minutes besides, since a watch can miss a change. launchd
    # watches the directory's entries only, so agent-plan touches the
    # directory after each write it makes; a plan file edited in place by
    # anything else is sent by the next run. Sync's lock keeps runs from
    # overlapping.
    launchd.agents.agent-plan-sync = mkIf pkgs.stdenv.hostPlatform.isDarwin {
      enable = true;
      config = {
        ProgramArguments = [(toString planSyncRunner) "--settle" "5s"];
        WatchPaths = [cfg.plans.dir];
        StartInterval = 300;
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

      timers.agent-plan-sync = {
        Unit.Description = "Sync agents' plans every 5 minutes";
        Timer = {
          OnActiveSec = "5min";
          OnUnitActiveSec = "5min";
        };
        Install.WantedBy = ["timers.target"];
      };

      services.agent-plan-sync = {
        Unit.Description = "Sync agents' plans with the notes repository";
        Service = {
          Type = "oneshot";
          Environment = ["PATH=${syncPath}"];
          ExecStart = "${planSyncRunner} --settle 5s";
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
      ++ [cfg.agentTools]
      ++ optional cfg.prLoop.enable (import ./review-loop.nix {
        inherit lib pkgs;
        # By program, as "git delegate" or "pr-loop", from patterns such as
        # "pr-loop open *".
        approved = unique (map (c: let
          w = splitString " " c;
        in
          if head w == "git"
          then "git ${elemAt w 1}"
          else head w)
        cfg.guard.excludedCommands);
      });
  };
}
