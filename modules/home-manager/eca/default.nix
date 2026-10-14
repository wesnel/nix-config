{
  config,
  lib,
  options,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.eca;

  llm = import ../llm/lib.nix {inherit lib;};

  mcpProxy = config.wgn.home.mcp-proxy;
  gondolin =
    (options ? home.programs.wgn.emacs.eca.sandbox.backend)
    && config.home.programs.wgn.emacs.eca.sandbox.enable
    && config.home.programs.wgn.emacs.eca.sandbox.backend == "gondolin";

  # One entry serves both sides. On this machine the URL falls back to the
  # proxy itself and the token is read from its file. In the guest the
  # wrapper sets MCP_PROXY_URL to a mapped name, the file is absent, and
  # MCP_PROXY_TOKEN holds Gondolin's placeholder, which it swaps for the real
  # token only on requests to the proxy.
  ecaMcpServers =
    mapAttrs (_: path: {
      url = "\${env:MCP_PROXY_URL:${mcpProxy.url}}${path}";
      headers.Authorization = "Bearer \${cmd:cat ${mcpProxy.tokenFile} 2>/dev/null || printenv MCP_PROXY_TOKEN}";
    })
    mcpProxy.endpoints;

  # Shell commands the review loop's agents may not run, as Java regexes that
  # ECA matches against the whole command string. ECA's deny rules win over
  # trust mode and remembered approvals. An agent's deny list replaces the
  # global one rather than adding to it, so a deny rule added to ECA's own
  # config has to be repeated here to apply to these agents. Add a rule to
  # the guard's policy.json, with examples, not here.
  loopDenied = pkgs.agent-tools.ecaDenied.coordinator;
  ownerDenied = pkgs.agent-tools.ecaDenied.owner;
  reviewerDenied = pkgs.agent-tools.ecaDenied.reviewer;

  # ECA rejects a command matching a deny pattern with no reason of ours,
  # and the patterns match a phrase anywhere in the command, so the agents
  # are told up front how to mention one. The policy.json examples that
  # break a phrase up are checked against the patterns at build time.
  deniedSearches = ''
    A shell command containing a denied phrase, such as `git push`,
    `--no-verify` or `push-guard.allowPublic`, is rejected "by user config"
    wherever the phrase appears: in a search pattern, a quoted string or a
    path as much as in the command itself. To search for or mention one,
    break the literal up: `grep -nE 'git\s+push'`, `grep -e '--no-verif[y]'`,
    `cat cmd/git-sign-'pass'/main.go`.
  '';
  deniedPhrases = ''
    ${deniedSearches}
    Write a commit message that mentions one to a file and pass it as
    `-m "$(cat FILE)"`.
  '';

  # ECA's git tool runs any git or gh command line and takes no argument
  # patterns, so it is hidden and the shell, which does, is used instead.
  # ECA anchors each entry as a regex against a built-in tool's bare name
  # and against any tool's `server__tool`.
  loopFrontmatter = disabled: denied:
    concatStringsSep "\n" ([
        "disabledTools:"
        "  - git"
        "  - '(?i).*linear.*__(?!(get|list|search|fetch|read|query)).*'"
      ]
      ++ map (t: "  - '${t}'") disabled
      ++ [
        "tools:"
        "  deny:"
      ]
      ++ map (p: "    - 'eca__shell_command(${p})'") denied);
in {
  options.wgn.home.eca = {
    enable = mkEnableOption "Configure my Editor Code Assistant skills and agents";

    prLoop.enable = mkOption {
      type = types.bool;
      default = true;

      description = ''
        Offers the bot review loop: the `pr-coordinator` agent and its
        `pr-owner` sub-agent, and, where the emacs-config sandbox is in use,
        `M-x wgn/eca-pr-loop` to start a sandboxed session with
        `sandbox-relay serve` beside it, so the loop reaches GitHub through
        `pr-loop` without the sandbox holding a credential. Ordinary sessions
        do not get the relay.
      '';
    };

    localModel = mkOption {
      type = types.nullOr types.str;
      default = null;
      example = "ollama/qwen2.5-coder:7b";

      description = ''
        Model backing the `local` agent, as `provider/model`. The model itself
        is pulled by hand; only the agent that selects it is declared here.

        It has to be one that calls tools natively. A model that instead
        describes the call in its reply -- which several coding models do,
        whatever capabilities they advertise -- leaves the agent with nothing
        to run, so it reads and edits nothing.

        Its context window and cost still have to be set under
        {option}`providers` in ECA's own config, which ECA owns: without them
        it treats the window as unbounded and never auto-compacts.

        ECA asks Ollama for thinking on every request, which Ollama refuses
        for a model that cannot think, so nothing is generated. Give such a
        model `"extraPayload": { "think": false }` beside its limits there.
        Leave `models.dev` unreachable: it has no Ollama provider, and once
        its catalogue is loaded ECA offers an `ollama/` model no tools.
      '';
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      wgn.home.llm.enable = true;

      # ECA discovers global skills under ~/.config/eca/skills rather than the
      # ~/.config/agents/skills that Amp reads, so this is its own directory
      # rather than a share.
      xdg.configFile =
        (llm.mkSkillFiles pkgs "eca/skills" config.wgn.home.llm.skills)
        // {
          # Which skills the wrapper may carry into the guest. It is a list
          # beside the skills rather than a mark inside each one, because a
          # skill directory is shared with the other agents and they have no
          # use for it -- and because home-manager links such a directory
          # whole, leaving nowhere to put a file the set does not declare.
          "eca/sandbox-skills.json".text =
            builtins.toJSON
            (llm.sandboxSkillNames config.wgn.home.llm.skills);

          # Carried into the sandbox with the rest of the config.
          "eca/AGENTS.md".source = config.wgn.home.llm.instructions;

          # Any agent may spawn it, the loop's coordinator included.
          "eca/agents/reviewer.md".text = ''
            ---
            mode: subagent
            inherit: code
            description: Reviews a change, the commits in a range BASE..TIP, against its goal statement, with fresh eyes. Spawn a new one for every review. Only reads.
            maxSteps: 100
            ${loopFrontmatter ["write_file" "edit_file" "move_file" "spawn_agent"] reviewerDenied}
            ---
            ${pkgs.agent-tools.reviewerPrompt}
            ${deniedSearches}
          '';
        };
    }

    (mkIf cfg.prLoop.enable (mkMerge [
      {
        wgn.home.llm.prLoop.enable = true;

        xdg.configFile = {
          "eca/agents/pr-coordinator.md".text = ''
            ---
            mode: primary
            inherit: code
            description: Runs the bot review loop over a stack of open pull requests, delegating each PR to a pr-owner.
            ${loopFrontmatter [] loopDenied}
            ---
            You coordinate the bot review loop. Load the `review-loop` skill
            before doing anything else, and follow it. Spawn one `pr-owner`
            per pull request for the work on its code, and a `reviewer`
            for each review of its fixups.

            ${deniedPhrases}
          '';

          # Each spawn starts a fresh chat, so the owner keeps what it learns
          # in the notes file the review-loop skill names.
          "eca/agents/pr-owner.md".text = ''
            ---
            mode: subagent
            inherit: code
            spawnableBy: pr-coordinator
            description: Owns one pull request of a stack during the bot review loop. Triages its bot threads and CI failures, fixes and restacks it in its own worktree, and answers questions about its code.
            maxSteps: 150
            ${loopFrontmatter [] ownerDenied}
            ---
            ${builtins.readFile ../llm/pr-loop/pr-owner.md}
            ${deniedPhrases}
          '';
        };
      }

      (optionalAttrs (options ? home.programs.wgn.emacs.eca.sandbox.prLoop) {
        home.programs.wgn.emacs.eca.sandbox = {
          prLoop = "${config.wgn.home.llm.prLoop.client}/bin";
          # Applied by either backend only in a review-loop session.
          args = concatMap (p: ["--protect" p]) config.wgn.home.llm.prLoop.protectedPaths;
        };
      })

      # An ordinary sandboxed session writes plans itself, at the path they
      # have here, and its guard hook reads the plans directory from the
      # same config as here, so the guard keeps the logs append-only in the
      # guest too. A loop session reaches plans only through the relay, so
      # the wrapper applies neither with --pr-loop.
      (optionalAttrs (options ? home.programs.wgn.emacs.eca.sandbox.plansDir) {
        home.programs.wgn.emacs.eca.sandbox = {
          plansDir = config.wgn.home.llm.plans.dir;
          guardConfig = "${config.xdg.configHome}/agent-tools/config.json";
        };
      })
    ]))

    # ~/.config/eca/agents is read and never written by ECA, so an agent can
    # be declared here without taking over the config file ECA writes itself.
    (mkIf (cfg.localModel != null) {
      # Not `explorer', the read-only agent the planner delegates
      # investigation to: an agent inheriting it cannot change a file.
      xdg.configFile."eca/agents/local.md".text = ''
        ---
        inherit: code
        description: Works in the codebase using a model running on this machine
        model: ${cfg.localModel}
        ---
      '';
    })

    # config.json is ECA's own file, so the servers are merged into it on
    # every switch rather than owned.
    (mkIf mcpProxy.enable {
      home.activation.ecaMcpServers = hm.dag.entryAfter ["writeBoundary"] ''
        conf="${config.xdg.configHome}/eca/config.json"
        servers=${escapeShellArg (builtins.toJSON ecaMcpServers)}

        if [ ! -e "$conf" ]; then
          current='{}'
        else
          current=$(cat "$conf")
        fi

        if merged=$(printf '%s' "$current" | ${pkgs.jq}/bin/jq --argjson servers "$servers" '.mcpServers = ((.mcpServers // {}) + $servers)'); then
          if [ "$merged" != "$current" ] && [ -z "''${DRY_RUN:-}" ]; then
            mkdir -p "$(dirname "$conf")"
            printf '%s\n' "$merged" > "$conf"
          fi
        else
          warnEcho "$conf is not plain JSON, so the MCP proxy was not registered with ECA."
        fi
      '';
    })

    # config.json is ECA's own file, so the hook that holds every session to
    # the baseline guard is merged into it on every switch rather than owned.
    # The guard reads each call's agent, so the loop's agents get their
    # roles' rules here too. It names `pr-loop` on PATH, which a sandboxed
    # session reaches only where the sandbox serves it.
    {
      home.activation.ecaGuard = hm.dag.entryAfter ["writeBoundary"] ''
        conf="${config.xdg.configHome}/eca/config.json"
        hook=${escapeShellArg (builtins.toJSON {
          type = "preToolCall";
          matcher = ".*";
          visible = false;
          description = "pr-loop guard: what is Wesley's to do";
          actions = [
            {
              type = "shell";
              shell = "pr-loop guard --baseline";
            }
          ];
        })}
        # Records commits, pushes, worktrees, branches and PRs in the plan
        # and asks which step a commit completes; after a push to GitHub,
        # names the `pr-loop wait ci` to start. ECA runs a postToolCall hook
        # only after a call that succeeded.
        post=${escapeShellArg (builtins.toJSON {
          type = "postToolCall";
          matcher = ".*";
          visible = false;
          description = "pr-loop guard --post: the plan's facts, and CI after a push";
          actions = [
            {
              type = "shell";
              shell = "pr-loop guard --post";
            }
          ];
        })}
        # spawn_agent returns a sub-agent's result to the coordinator: asks
        # it to check the report and log the part. subagentStart and
        # subagentPostRequest speak only to the sub-agent.
        subagent=${escapeShellArg (builtins.toJSON {
          type = "postToolCall";
          matcher = "eca__spawn_agent";
          visible = false;
          description = "agent-plan remind: check a sub-agent's report";
          actions = [
            {
              type = "shell";
              shell = "agent-plan remind";
            }
          ];
        })}
        # preCompact can add no context, only block, so after a compaction
        # asks for the plan to be brought up to date.
        compact=${escapeShellArg (builtins.toJSON {
          type = "postCompact";
          visible = false;
          description = "agent-plan remind: the plan after a compaction";
          actions = [
            {
              type = "shell";
              shell = "agent-plan remind";
            }
          ];
        })}

        if [ ! -e "$conf" ]; then
          current='{}'
        else
          current=$(cat "$conf")
        fi

        if merged=$(printf '%s' "$current" | ${pkgs.jq}/bin/jq --argjson hook "$hook" --argjson post "$post" --argjson subagent "$subagent" --argjson compact "$compact" '.hooks."guard-baseline" = $hook | .hooks."guard-post" = $post | .hooks."plan-subagent" = $subagent | .hooks."plan-compact" = $compact'); then
          if [ "$merged" != "$current" ] && [ -z "''${DRY_RUN:-}" ]; then
            mkdir -p "$(dirname "$conf")"
            printf '%s\n' "$merged" > "$conf"
          fi
        else
          warnEcho "$conf is not plain JSON, so the guard was not registered with ECA."
        fi
      '';
    }

    # Only Gondolin can map a host service and inject a secret into the guest.
    (optionalAttrs (options ? home.programs.wgn.emacs.eca.sandbox.secretFiles) (mkIf (mcpProxy.enable && gondolin) {
      home.programs.wgn.emacs.eca.sandbox = {
        secretFiles.MCP_PROXY_TOKEN = mcpProxy.tokenFile;
        args = [
          "--http-map"
          "mcp-proxy:${toString mcpProxy.port}=127.0.0.1:${toString mcpProxy.port}"
          "--env"
          "MCP_PROXY_URL=http://mcp-proxy:${toString mcpProxy.port}"
          # Matched against the host after the map rewrites it, so this is
          # the upstream's; naming `mcp-proxy` instead gets the request
          # refused. Every service mapped to 127.0.0.1 can therefore be sent
          # the token, all of them on this machine.
          "--secret"
          "MCP_PROXY_TOKEN=127.0.0.1"
        ];
      };
    }))
  ]);
}
