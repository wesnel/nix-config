{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.codex;

  llm = import ../llm/lib.nix {inherit lib;};

  mcpProxy = config.wgn.home.mcp-proxy;

  registerMcpServers = pkgs.writers.writePython3 "codex-mcp-servers" {libraries = [pkgs.python3Packages.tomlkit];} ''
    import json
    import os
    import sys

    import tomlkit

    path = sys.argv[1]
    servers = json.loads(sys.argv[2])

    doc = tomlkit.document()
    if os.path.exists(path):
        with open(path) as f:
            doc = tomlkit.parse(f.read())

    tables = doc.setdefault("mcp_servers", tomlkit.table(is_super_table=True))
    changed = False
    for name, entry in servers.items():
        if tables.get(name) != entry:
            table = tomlkit.table()
            table.update(entry)
            tables[name] = table
            changed = True

    if changed:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            f.write(tomlkit.dumps(doc))
  '';

  # Makes a path the one writable root of Codex's workspace-write sandbox
  # that this module manages, keeping the rest of the file and any roots set
  # from inside Codex. The third argument records the root added last time,
  # which is removed when the path changes; no other root is removed.
  addWritableRoot = pkgs.writers.writePython3 "codex-writable-root" {libraries = [pkgs.python3Packages.tomlkit];} ''
    import os
    import sys

    import tomlkit

    path, root, state = sys.argv[1], sys.argv[2], sys.argv[3]

    previous = None
    if os.path.exists(state):
        with open(state) as f:
            previous = f.read().strip() or None

    doc = tomlkit.document()
    if os.path.exists(path):
        with open(path) as f:
            doc = tomlkit.parse(f.read())

    table = doc.setdefault("sandbox_workspace_write", tomlkit.table())
    roots = table.setdefault("writable_roots", tomlkit.array())
    changed = False
    if previous and previous != root:
        for i in reversed(range(len(roots))):
            if str(roots[i]) == previous:
                del roots[i]
                changed = True
    if root not in [str(r) for r in roots]:
        roots.append(root)
        changed = True
    if changed:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            f.write(tomlkit.dumps(doc))

    os.makedirs(os.path.dirname(state), exist_ok=True)
    with open(state, "w") as f:
        f.write(root + "\n")
  '';

  # Sets keys of config.toml's [notice] table, keeping the rest of the file.
  setNotices = pkgs.writers.writePython3 "codex-notices" {libraries = [pkgs.python3Packages.tomlkit];} ''
    import json
    import os
    import sys

    import tomlkit

    path = sys.argv[1]
    notices = json.loads(sys.argv[2])

    doc = tomlkit.document()
    if os.path.exists(path):
        with open(path) as f:
            doc = tomlkit.parse(f.read())

    table = doc.setdefault("notice", tomlkit.table())
    changed = False
    for key, value in notices.items():
        if table.get(key) != value:
            table[key] = value
            changed = True

    if changed:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            f.write(tomlkit.dumps(doc))
  '';

  plans = config.wgn.home.llm.plans;

  toml = value:
    if isAttrs value
    then "{${concatStringsSep ", " (mapAttrsToList (k: v: "${builtins.toJSON k}=${toml v}") value)}}"
    else if isList value
    then "[${concatMapStringsSep ", " toml value}]"
    else builtins.toJSON value;

  # Passed as -c overrides, because Codex writes ~/.codex/config.toml itself.
  # A `sandbox_mode` there, or --sandbox, makes Codex ignore permission
  # profiles, so neither may be used with this launcher. "{socket}" is the
  # relay's socket, filled in by `sandbox-relay session`, and $HOME is
  # expanded by the shell.
  overrides = {
    default_permissions = "pr_loop";
    # Domain rules and Unix sockets are only enforced through the proxy.
    "features.network_proxy.enabled" = true;
    "permissions.pr_loop.extends" = ":workspace";
    "permissions.pr_loop.network.enabled" = true;
    "permissions.pr_loop.network.unix_sockets" = {"{socket}" = "allow";};
    # A hook that is new or changed is skipped until trusted with /hooks, so
    # it names `pr-loop` on PATH rather than a store path that changes with
    # every rebuild.
    "hooks.PreToolUse" = [
      {
        matcher = ".*";
        hooks = [
          {
            type = "command";
            command = "pr-loop guard";
          }
        ];
      }
    ];
  };

  domains = list: escapeShellArg "permissions.pr_loop.network.domains=${toml (genAttrs list (_: "allow"))}";

  prLoopLauncher = pkgs.writeShellApplication {
    name = "codex-pr-loop";
    text = ''
      # The more specific path wins, so the home directory is denied but for
      # the reads allowed below it. A read rule replaces write access, so
      # none may name the workspace, the cache directory or the git
      # directory, which is usually the workspace's own .git, or the
      # workspace itself.
      filesystem="${concatStringsSep ", " (
        [''\"${config.home.homeDirectory}\"=\"deny\"'']
        ++ map (p: ''\"${p}\"=\"read\"'') (subtractLists ["{workspace}" "{gitdir}" config.wgn.home.llm.prLoop.cacheDir] config.wgn.home.llm.prLoop.allowedReads)
        ++ map (p: ''\"${p}\"=\"deny\"'') config.wgn.home.llm.prLoop.deniedReads
        ++ map (p: ''\"{workspace}/${p}\"=\"read\"'') config.wgn.home.llm.prLoop.protectedPaths
        ++ [''\"${config.wgn.home.llm.prLoop.cacheDir}\"=\"write\"'']
        # Reached through `agent-plan` on the relay.
        ++ [''\"${plans.dir}\"=\"read\"'']
      )}"
      domains=${domains config.wgn.home.llm.prLoop.allowedDomains}
      ${config.wgn.home.llm.prLoop.reviewSession}
      if [[ -n "$review" ]]; then
        domains=${domains config.wgn.home.llm.prLoop.reviewAllowedDomains}
      fi
      ${config.wgn.home.llm.prLoop.sandboxCaches}
      # The review-loop skill starts only where this is set.
      export PR_LOOP_SESSION=1
      # The session's commands cannot read the user's git config; the relay
      # and pr-loop on this side keep it.
      ${config.wgn.home.llm.prLoop.gitIdentity}
      exec ${lib.getExe' config.wgn.home.llm.agentTools "sandbox-relay"} session --program pr-loop --program agent-plan -- \
        env GIT_CONFIG_GLOBAL=${config.wgn.home.llm.prLoop.gitConfig} codex \
        ${concatStringsSep " \\\n  " (mapAttrsToList (k: v: "-c ${escapeShellArg "${k}=${toml v}"}") overrides)} \
        -c "$domains" \
        -c "permissions.pr_loop.filesystem={$filesystem}" \
        "$@"
    '';
  };
in {
  options.wgn.home.codex = {
    enable = mkEnableOption "Enables my Codex skills for home-manager";

    prLoop.enable = mkOption {
      type = types.bool;
      default = true;

      description = ''
        Installs `codex-pr-loop`, which starts Codex for the unattended bot
        review loop: its commands run in Codex's sandbox without GitHub
        credentials or a route to GitHub, reaching it only through `pr-loop`
        on a relay, and `pr-loop guard` checks each tool call once trusted with
        `/hooks`.
      '';
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      wgn.home.llm.enable = true;

      # Codex is installed and updated by itself, and writes ~/.codex/config.toml
      # to record project trust, MCP servers and model choice. Generating that
      # file is what stops it being able to.
      home.file =
        llm.mkSkillFiles pkgs ".codex/skills" config.wgn.home.llm.skills
        // {
          # Codex skips this when an AGENTS.override.md beside it is non-empty.
          ".codex/AGENTS.md".source = config.wgn.home.llm.instructions;

          # Holds every session to the baseline guard. Codex skips a hook
          # until it is trusted with /hooks, and trust follows the hook's
          # hash, so the command names `pr-loop` on PATH rather than a store
          # path that changes with every rebuild. Codex adds these to any
          # other layer's hooks, codex-pr-loop's included.
          ".codex/hooks.json".text = builtins.toJSON {
            # Records commits, pushes, worktrees, branches and PRs in the
            # plan and asks which step a commit completes; after a push to
            # GitHub, names the `pr-loop wait ci` to start; hands over the
            # pull-requests skill when a PR is opened or edited, and the
            # commit rules on the session's first commit. Codex names every
            # shell call Bash.
            hooks.PostToolUse = [
              {
                matcher = "Bash";
                hooks = [
                  {
                    type = "command";
                    command = "pr-loop guard --post";
                  }
                ];
              }
              # wait_agent hands the coordinator a sub-agent's result: asks
              # it to check the report and log the part. SubagentStop's
              # output reaches only the sub-agent.
              {
                matcher = "wait_agent";
                hooks = [
                  {
                    type = "command";
                    command = "agent-plan remind";
                  }
                ];
              }
            ];
            # PreCompact can add no context, so after a compaction
            # (source "compact", which agent-plan checks) asks for the plan
            # to be brought up to date.
            hooks.SessionStart = [
              {
                hooks = [
                  {
                    type = "command";
                    command = "agent-plan remind";
                  }
                ];
              }
            ];
            # The operator's prompt: its URLs become plan references. A
            # sub-agent's turn carries agent_id, which agent-plan skips.
            # Carries the time and how long since the operator's previous
            # message.
            hooks.UserPromptSubmit = [
              {
                hooks = [
                  {
                    type = "command";
                    command = "agent-plan remind";
                  }
                ];
              }
            ];
            hooks.PreToolUse = [
              {
                matcher = ".*";
                hooks = [
                  {
                    type = "command";
                    command = "pr-loop guard --baseline";
                  }
                ];
              }
            ];
          };
        };
    }

    (mkIf cfg.prLoop.enable {
      wgn.home.llm.prLoop.enable = true;
      home.packages = [prLoopLauncher];
    })

    # Plans are written with `agent-plan` and the file tools, from inside
    # the workspace-write sandbox too. Merged into the config.toml Codex
    # writes, as the MCP servers are.
    {
      home.activation.codexPlansRoot = lib.hm.dag.entryAfter ["writeBoundary"] ''
        if [ -z "''${DRY_RUN:-}" ]; then
          ${addWritableRoot} "$HOME/.codex/config.toml" ${escapeShellArg plans.dir} \
            ${escapeShellArg "${config.xdg.stateHome}/codex/plans-root"} \
            || warnEcho "The plans directory was not made writable in Codex's sandbox."
        fi
      '';
    }

    # Near a rate limit Codex offers to switch the running session to a
    # cheaper model, which is the operator's call alone. Merged into the
    # config.toml Codex writes.
    {
      home.activation.codexNotices = lib.hm.dag.entryAfter ["writeBoundary"] ''
        if [ -z "''${DRY_RUN:-}" ]; then
          ${setNotices} "$HOME/.codex/config.toml" ${escapeShellArg (builtins.toJSON {hide_rate_limit_model_nudge = true;})} \
            || warnEcho "Codex's offer to switch models near a rate limit was not hidden."
        fi
      '';
    }

    # Merged into the config.toml Codex writes, keeping its formatting and
    # everything else in it. Each named entry is replaced whole.
    # http_headers_helper reads the token when Codex connects.
    (mkIf mcpProxy.enable {
      home.activation.codexMcpServers = lib.hm.dag.entryAfter ["writeBoundary" "mcpProxyToken"] ''
        if [ -z "''${DRY_RUN:-}" ]; then
          ${registerMcpServers} "$HOME/.codex/config.toml" ${escapeShellArg (builtins.toJSON (mapAttrs (_: path: {
            url = mcpProxy.url + path;
            http_headers_helper = mcpProxy.headersHelper;
          })
          mcpProxy.endpoints))} \
            || warnEcho "The MCP proxy was not registered with Codex."
        fi
      '';
    })
  ]);
}
