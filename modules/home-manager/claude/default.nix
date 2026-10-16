{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.claude;

  llm = import ../llm/lib.nix {inherit lib;};

  # Loaded only by claude-pr-loop, so the loop's guard never applies to an
  # ordinary session, which has the baseline guard (see settings). The
  # review-loop skill is shared with the other agents, and the loop's
  # reviewer is the user-level `reviewer` agent.
  prLoopPlugin = pkgs.runCommand "claude-pr-loop-plugin" {} ''
    mkdir -p $out/.claude-plugin $out/hooks $out/agents
    cp ${pkgs.writeText "pr-owner.md" ''
      ---
      name: pr-owner
      description: Owns one pull request of a stack during the bot review loop. Triages its bot threads and CI failures, fixes and restacks it in its own worktree, and answers questions about its code.
      ---
      ${builtins.readFile ../llm/pr-loop/pr-owner.md}
    ''} $out/agents/pr-owner.md
    cp ${pkgs.writeText "plugin.json" (builtins.toJSON {
      name = "pr-loop";
      description = "Guards and the PR owner agent for the unattended bot review loop";
    })} $out/.claude-plugin/plugin.json
    cp ${pkgs.writeText "hooks.json" (builtins.toJSON {
      hooks.PreToolUse = [
        {
          matcher = "Bash|Monitor|mcp__.*";
          hooks = [
            {
              type = "command";
              command = "${lib.getExe' config.wgn.home.llm.agentTools "pr-loop"} guard";
            }
          ];
        }
      ];
    })} $out/hooks/hooks.json
  '';

  darwin = pkgs.stdenv.hostPlatform.isDarwin;
  mcpProxy = config.wgn.home.mcp-proxy;
  plans = config.wgn.home.llm.plans;
  agentPlan = getExe' config.wgn.home.llm.agentTools "agent-plan";

  # A permission rule's path: Claude Code reads `~/` as the home directory,
  # and a lone `/` as relative to the settings file, so `//` starts an
  # absolute one.
  rulePath = path: let
    home = "${config.home.homeDirectory}/";
  in
    if hasPrefix home path
    then "~/${removePrefix home path}"
    else "/${path}";

  # Build caches for every session's commands, which the sandbox lets them
  # write. Builds outside Claude Code do not read them: a cache entry written
  # by an agent would otherwise be compiled into a later build on the host.
  sessionCacheDir = "${config.xdg.cacheHome}/claude-code";
  sessionCacheEnv = {
    GOCACHE = "${sessionCacheDir}/go-build";
    # Holds the module cache, and the checksum database state that `go get`
    # writes. `go install` installs here too, off PATH.
    GOPATH = "${sessionCacheDir}/go";
    GOLANGCI_LINT_CACHE = "${sessionCacheDir}/golangci-lint";
  };

  # The session's commands run in Claude Code's sandbox with no credentials and
  # no route to GitHub; `sandbox-relay session` serves `pr-loop`,
  # `agent-plan` and `git-fresh-review` on a private socket, whose path replaces {socket}. Hooks, MCP servers and WebFetch run outside
  # the sandbox, which is why WebFetch to GitHub is denied as well.
  prLoopSettings = allowedDomains: builtins.toJSON {
    # The user settings' `env` would otherwise replace the launcher's exports
    # with the ordinary sessions' caches.
    env = config.wgn.home.llm.prLoop.cacheEnv;

    permissions = {
      # Without this the auto-mode classifier may stop the force-updates that
      # `pr-loop push` makes, which is the one write the loop exists to do.
      allow = ["Bash(pr-loop *)"];
      deny = ["WebFetch(domain:github.com)" "WebFetch(domain:*.github.com)"];
    };

    sandbox = {
      enabled = true;
      # The model cannot ask to rerun a command outside the sandbox, and the
      # session refuses to start where no sandbox is available.
      allowUnsandboxedCommands = false;
      failIfUnavailable = true;

      # The user settings open trustd, which runs outside the sandbox and
      # fetches revocation and issuer data named by the certificates it is
      # handed, past the allowlist below.
      enableWeakerNetworkIsolation = false;

      network =
        {
          inherit allowedDomains;
          strictAllowlist = true;
        }
        # macOS can allow one socket. Linux allows every socket, so the agent
        # sockets are hidden below instead.
        // (
          if darwin
          then {allowUnixSockets = ["{socket}"];}
          else {allowAllUnixSockets = true;}
        );

      # All of the home directory but what the session needs: secrets that
      # no list names, such as other agents' logins, stay out too.
      filesystem.denyRead = [config.home.homeDirectory] ++ config.wgn.home.llm.prLoop.deniedReads;
      filesystem.allowRead = config.wgn.home.llm.prLoop.allowedReads;

      # Relative paths here are not resolved against the workspace, so
      # `sandbox-relay session` fills in its real path. The user settings'
      # write grants merge into this session's, so the ordinary sessions'
      # caches, which commands outside this sandbox build from, are taken
      # back, as are the plans, which this session reaches through
      # `agent-plan` on the relay.
      filesystem.denyWrite =
        map (p: "{workspace}/${p}") config.wgn.home.llm.prLoop.protectedPaths
        ++ [sessionCacheDir plans.dir];

      # The launcher points the build caches here.
      filesystem.allowWrite = [config.wgn.home.llm.prLoop.cacheDir];

      credentials = {
        files = [
          {
            path = "~/.config/gh/hosts.yml";
            mode = "deny";
          }
        ];
        envVars =
          map (name: {
            inherit name;
            mode = "deny";
          }) ["GH_TOKEN" "GITHUB_TOKEN" "SSH_AUTH_SOCK"];
      };
    };

    # A further, judgement-based layer: the classifier is told what this
    # session is for. It does not replace the sandbox.
    autoMode = {
      environment = [
        "$defaults"
        "This is an unattended review-loop session over open pull requests. Its only route to GitHub is the `pr-loop` command, which enforces the loop's policy."
      ];
      hard_deny = [
        "$defaults"
        "Reading credentials: GitHub tokens, keychain items, SSH or GPG keys, or any token in the environment or on disk."
        "Contacting GitHub, or sending repository content anywhere, other than through `pr-loop`."
        "Marking a pull request ready for review, merging it, or commenting on it outside a review bot's thread."
      ];
    };
  };

  prLoopCaches = config.wgn.home.llm.prLoop.sandboxCaches;

  prLoopLauncher = pkgs.writeShellApplication {
    name = "claude-pr-loop";
    text = ''
      ${prLoopCaches}
      # The review-loop skill starts only where this is set.
      export PR_LOOP_SESSION=1
      # "$defaults" in the settings is literal: Claude Code splices its own
      # auto-mode rules in there.
      # shellcheck disable=SC2016
      settings=${lib.escapeShellArg (prLoopSettings config.wgn.home.llm.prLoop.allowedDomains)}
      ${config.wgn.home.llm.prLoop.reviewSession}
      if [[ -n "$review" ]]; then
        # shellcheck disable=SC2016
        settings=${lib.escapeShellArg (prLoopSettings config.wgn.home.llm.prLoop.reviewAllowedDomains)}
      fi
      # The session's commands cannot read the user's git config; the relay
      # and pr-loop on this side keep it.
      ${config.wgn.home.llm.prLoop.gitIdentity}
      exec ${lib.getExe' config.wgn.home.llm.agentTools "sandbox-relay"} session --program pr-loop --program agent-plan \
        --program git-fresh-review -- \
        env GIT_CONFIG_GLOBAL=${config.wgn.home.llm.prLoop.gitConfig} claude \
        --plugin-dir ${prLoopPlugin} \
        --settings "$settings" \
        --permission-mode auto \
        "$@"
    '';
  };
in {
  options.wgn.home.claude = {
    enable = mkEnableOption "Enables my Claude Code skills and context for home-manager";

    prLoop.enable = mkOption {
      type = types.bool;
      default = true;

      description = ''
        Installs `claude-pr-loop`, which starts Claude Code for the unattended
        bot review loop: its commands run in Claude Code's sandbox, without
        credentials, reaching GitHub only through `pr-loop` on a relay, and hooks
        deny other writes to GitHub and Linear. On Linux the sandbox needs
        bubblewrap and socat on PATH; without them the session will not start.
      '';
    };

    settings = mkOption {
      type = types.attrs;

      description = ''
        Keys merged into ~/.claude/settings.json on every switch. The file stays
        Claude Code's, so settings changed from inside it are kept, but a key
        set here returns to this value at the next switch.
      '';

      default = {
        # Shared instructions forbid attribution trailers; this stops Claude
        # Code adding them, in the form every version accepts.
        attribution = {
          commit = "";
          pr = "";
          sessionUrl = false;
        };

        # These reach every subprocess, sandboxed or not. claude-pr-loop sets
        # its own.
        env = sessionCacheEnv;

        # Credentials stay out of every session. The guard denies shell
        # commands and file tools naming them; Read is not hooked, so it is
        # denied here. Each is a path prefix, so a rule matches a file
        # whose name it starts, and another anything under a directory
        # whose name it starts.
        permissions.deny =
          concatMap (p: ["Read(${rulePath p}*)" "Read(${rulePath p}*/**)"]) config.wgn.home.llm.guard.credentialPaths
          # A plan's log is appended to by `agent-plan log` alone; the guard
          # denies the same files to every file tool and shell write. An Edit
          # rule covers Write too.
          ++ map (p: "Edit(${rulePath plans.dir}/${p})") ["*.log.org" "*.log.org.lock"];

        # The auto-mode classifier judges intent, so it also stops a rule
        # broken where the guard cannot see, as through a script; the guard
        # stays the enforcement. Claude Code adds claude-pr-loop's own
        # rules to these. "$defaults" is literal: Claude Code splices its
        # own rules in there.
        autoMode.hard_deny = [
          "$defaults"
          "Reading credentials: SSH or GPG private keys, agents' logins, keychain items, tokens on disk, or the environment's secrets."
          "Pushing to GitHub, or changing anything there (pull requests, issues, comments, reviews, releases, settings), other than through `pr-loop`."
          "Running anything as root: sudo, doas, su, or an administrator prompt."
          "Changing an agent's settings or hooks, or the guard's config: Claude Code's, Codex's or ECA's settings and hooks files, or agent-tools/config.json; or turning hooks or permission checks off."
          "Publishing a package, container image or release."
          "Running a command with NGROK_ENV set to prod or stage, however it is set."
        ];

        # Applies wherever a project turns the sandbox on, and merges into
        # claude-pr-loop's sandbox, which denies what it must not have.
        sandbox =
          {
            # `trash` moves a file on the home volume into ~/.Trash, which the
            # trash skill asks for in place of rm. Plans are written with
            # `agent-plan` and the file tools.
            filesystem.allowWrite =
              [sessionCacheDir plans.dir]
              ++ optional darwin "${config.home.homeDirectory}/.Trash";

            # Only the watcher's sync pushes plans.
            filesystem.denyRead = [plans.repository.key];
          }
          // optionalAttrs darwin {
            # Go programs, gh and the go command among them, verify TLS
            # through trustd on macOS, which they fail to reach otherwise.
            # This opens the trust service alone, not the keychain.
            enableWeakerNetworkIsolation = true;
          };

        # A plan approved in plan mode is recorded in the session's plan.
        # The response's `plan` and `filePath` are ExitPlanMode's output
        # schema; agent-plan falls back to the input's `plan`.
        hooks.PostToolUse = [
          {
            matcher = "ExitPlanMode";
            hooks = [
              {
                type = "command";
                command = "${agentPlan} import-claude";
              }
            ];
          }
          # Records commits, pushes, worktrees, branches and PRs in the plan
          # and asks which step a commit completes; after a push to GitHub,
          # names the `pr-loop wait ci` to start; hands over the
          # pull-requests skill when a PR is opened or edited, and the
          # commit rules on the session's first commit.
          {
            matcher = "Bash";
            hooks = [
              {
                type = "command";
                command = "${getExe' config.wgn.home.llm.agentTools "pr-loop"} guard --post";
              }
            ];
          }
          # A foreground sub-agent's result: asks the coordinator to check
          # it and log the part. A background one's arrives as a prompt.
          {
            matcher = "Agent|Task";
            hooks = [
              {
                type = "command";
                command = "${agentPlan} remind";
              }
            ];
          }
        ];

        # A background sub-agent's task-notification reaches the coordinator
        # as a prompt; SubagentStop's output would reach only the sub-agent.
        # Any other prompt is the operator's: its URLs become plan references.
        # Either asks once per quota threshold whether to keep going or
        # postpone, and carries the time and how long since the operator's
        # previous message.
        hooks.UserPromptSubmit = [
          {
            hooks = [
              {
                type = "command";
                command = "${agentPlan} remind";
              }
            ];
          }
        ];

        # Its output becomes the compaction's instructions: keep what the
        # plan does not yet record. Exit 2 would block the compaction.
        hooks.PreCompact = [
          {
            hooks = [
              {
                type = "command";
                command = "${agentPlan} remind";
              }
            ];
          }
        ];

        # Names the session's plans when it resumes or compacts, matched by
        # its session_id, which both keep, or by its worktree.
        hooks.SessionStart = [
          {
            matcher = "compact|resume";
            hooks = [
              {
                type = "command";
                command = "${agentPlan} find --hook";
              }
            ];
          }
        ];

        # When Claude Code waits for the operator, an OSC 777 notification,
        # which Ghostty shows on the machine displaying the terminal, over ssh
        # too. Not on Stop, which would notify after every turn.
        hooks.Notification = [
          {
            hooks = [
              {
                type = "command";
                command = ''printf '\033]777;notify;Claude Code;%s\a' "$(${getExe pkgs.jq} -r '.message // "Waiting for input"')"'';
              }
            ];
          }
        ];

        # Blocks a stop once when the work is not as the plan-work and
        # review-change skills leave it; never while stop_hook_active.
        hooks.Stop = [
          {
            hooks = [
              {
                type = "command";
                command = "${agentPlan} stop-check";
              }
            ];
          }
        ];

        # How many open questions the session's plan has, from its files
        # alone, which the `questions` skill asks the operator.
        # Context and quota too; the rerun drops a window once it resets.
        statusLine = {
          type = "command";
          command = "${agentPlan} statusline";
          refreshInterval = 60;
        };

        # A hook list set here replaces the file's own on every switch.
        hooks.PreToolUse = [
          # Holds every session to the baseline: what is the operator's to do,
          # and the `reviewer` and `pr-owner` agents to their roles. Every
          # tool that runs a command or writes a file belongs here: Monitor
          # runs a shell command as Bash does, and ExitWorktree can delete a
          # worktree and its branch. MCP tools are held to the guard's
          # mcp.json: on a server it lists, reads pass, listed writes ask and
          # the rest are refused; every tool on a server it does not list
          # asks, and is refused to the `reviewer` and `pr-owner` agents.
          {
            matcher = "Bash|Monitor|ExitWorktree|Edit|Write|MultiEdit|NotebookEdit|mcp__.*";
            hooks = [
              {
                type = "command";
                command = "${getExe' config.wgn.home.llm.agentTools "pr-loop"} guard --baseline";
              }
            ];
          }
        ];
      };
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      wgn.home.llm.enable = true;

      # Claude Code is installed and updated by itself, and owns ~/.claude.json
      # and ~/.claude/settings.json. Only the files it reads without writing
      # are kept here.
      home.file =
        llm.mkSkillFiles pkgs ".claude/skills" config.wgn.home.llm.skills
        // {
          ".claude/CLAUDE.md".source = config.wgn.home.llm.instructions;

          # Only reads: it has no editing tools, and the guard hook above
          # denies it every command that changes something.
          ".claude/agents/reviewer.md".text = ''
            ---
            name: reviewer
            description: Reviews a change, the commits in a range BASE..TIP, against its goal statement, with fresh eyes. Start a new one for every review. Only reads.
            tools: Read, Grep, Glob, Bash
            ---
            ${pkgs.agent-tools.reviewerPrompt}
          '';

          # Run only as /questions, so it costs nothing until the operator
          # asks.
          # AskUserQuestion takes one to four questions of two to four
          # options each, and adds "Other" itself.
          ".claude/skills/questions/SKILL.md".text = ''
            ---
            name: questions
            description: Ask the operator the open questions of this session's plan, and record their answers as decisions.
            disable-model-invocation: true
            ---

            # Questions

            1. Run `agent-plan find`; its first result's `plan` is PLAN. With
               none, say this session has no plan, and stop.
            2. Run `agent-plan questions PLAN --all --json`: the questions
               the operator can answer now. With none, say so, and stop.
            3. Ask them with AskUserQuestion, in order, at most four per call.
               Each question's header is its `id`, then ` [#P]` with its
               `priority` if it has one; its text its `question`, then its
               `context` if any. Its options: the `recommendation` first,
               labelled with " (Recommended)" after it, then its
               `alternatives`, then "Drop it", four options at most, dropping
               alternatives from the end to fit.
            4. For "Drop it", run
               `agent-plan decide PLAN ID --drop --answer 'dropped by the operator'`,
               or with their reason if they gave one. For any other answer, run
               `agent-plan decide PLAN ID --answer 'ANSWER'`, the answer on one
               line, without " (Recommended)".
            5. A decision can make a question that waited for it askable:
               run step 2 again, and ask from step 3 the questions it lists
               that were not asked yet, until it lists no new one.
            6. Report each decision in one line: its ID, the question, the
               answer.
          '';
        };

      # The Linux sandbox skips a write grant whose path does not exist.
      home.activation.claudeSessionCaches = lib.hm.dag.entryAfter ["writeBoundary"] ''
        run mkdir -p ${escapeShellArg sessionCacheDir}
      '';

      # Merged rather than linked: a read-only settings.json would make every
      # change made from inside Claude Code last only for that session.
      home.activation.claudeSettings = lib.hm.dag.entryAfter ["writeBoundary"] ''
        settings="$HOME/.claude/settings.json"
        patch=${escapeShellArg (builtins.toJSON cfg.settings)}

        if [ -L "$settings" ]; then
          warnEcho "$settings is a link, so the declared Claude Code settings were not merged into it."
        elif [ ! -e "$settings" ]; then
          if [ -z "''${DRY_RUN:-}" ]; then
            mkdir -p "$(dirname "$settings")"
            ${pkgs.jq}/bin/jq -n --argjson patch "$patch" '$patch' > "$settings"
          fi
        elif merged=$(${pkgs.jq}/bin/jq --argjson patch "$patch" '. * $patch' "$settings"); then
          if [ "$merged" != "$(cat "$settings")" ]; then
            run cp "$settings" "$settings.before-home-manager"
            [ -n "''${DRY_RUN:-}" ] || printf '%s\n' "$merged" > "$settings"
          fi
        else
          warnEcho "$settings is not valid JSON, so the declared Claude Code settings were not merged into it."
        fi
      '';
    }

    (mkIf cfg.prLoop.enable {
      wgn.home.llm.prLoop.enable = true;
      home.packages = [prLoopLauncher];
    })

    # User-scoped servers live in ~/.claude.json, which Claude Code rewrites
    # constantly, so these entries are merged in on every switch rather than
    # owned. headersHelper reads the token when Claude Code connects.
    (mkIf mcpProxy.enable {
      home.activation.claudeMcpServers = lib.hm.dag.entryAfter ["writeBoundary" "mcpProxyToken"] ''
        state="$HOME/.claude.json"
        servers=${escapeShellArg (builtins.toJSON (mapAttrs (_: path: {
            type = "http";
            url = mcpProxy.url + path;
            inherit (mcpProxy) headersHelper;
          })
          mcpProxy.endpoints))}

        if [ ! -e "$state" ]; then
          verboseEcho "$state does not exist yet; the MCP proxy is registered at the next switch after Claude Code first runs."
        elif merged=$(${pkgs.jq}/bin/jq --argjson servers "$servers" '.mcpServers = ((.mcpServers // {}) + $servers)' "$state"); then
          if [ "$merged" != "$(cat "$state")" ] && [ -z "''${DRY_RUN:-}" ]; then
            printf '%s\n' "$merged" > "$state"
          fi
        else
          warnEcho "$state is not valid JSON, so the MCP proxy was not registered with Claude Code."
        fi
      '';
    })
  ]);
}
