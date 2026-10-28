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

  # Sets config.toml's top-level keys and its tables' keys from a JSON
  # object, keeping the rest of the file.
  setKeys = pkgs.writers.writePython3 "codex-set-keys" {libraries = [pkgs.python3Packages.tomlkit];} ''
    import json
    import os
    import sys

    import tomlkit

    path = sys.argv[1]
    keys = json.loads(sys.argv[2])

    doc = tomlkit.document()
    if os.path.exists(path):
        with open(path) as f:
            doc = tomlkit.parse(f.read())

    changed = False
    for key, value in keys.items():
        if isinstance(value, dict):
            table = doc.setdefault(key, tomlkit.table())
            for k, v in value.items():
                if table.get(k) != v:
                    table[k] = v
                    changed = True
        elif doc.get(key) != value:
            doc[key] = value
            changed = True

    if changed:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            f.write(tomlkit.dumps(doc))
  '';

  plans = config.wgn.home.llm.plans;
in {
  options.wgn.home.codex = {
    enable = mkEnableOption "Enables my Codex skills for home-manager";

    sandbox = mkOption {
      type = types.attrs;
      internal = true;
      default = {
        sandbox_mode = "workspace-write";
        sandbox_workspace_write.network_access = false;
      };

      description = ''
        The sandbox every interactive Codex session's commands run in,
        merged into ~/.codex/config.toml on every switch, so that it never
        rests on what Codex chooses for a trusted project. An assertion
        holds it to workspace-write with the network off.
      '';
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      wgn.home.llm.enable = true;
      wgn.home.llm.prLoop.enable = mkDefault true;

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
          # other layer's hooks.
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

    # Plans are written with `agent-plan`, from inside the workspace-write
    # sandbox too, so the plans directory stays writable there. Claude
    # Code's sandbox denies it to every command but agent-plan's writes;
    # Codex's sandbox cannot tell agent-plan from any other command, so in
    # Codex the guard's plan-log and plan-file rules alone keep the shell
    # and the file tools off the plans. Merged into the config.toml Codex
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

    # Pins interactive sessions' sandbox: commands write only the workspace
    # and the writable roots above, and reach no network; the guard hook
    # holds the rest of what Codex may do. Codex reads `sandbox_mode` and
    # `[sandbox_workspace_write] network_access` from config.toml; git
    # delegate's `codex exec --ignore-user-config` reads neither.
    # Merged into the config.toml Codex writes.
    {
      assertions = [
        {
          assertion =
            (cfg.sandbox.sandbox_mode or null)
            == "workspace-write"
            && (cfg.sandbox.sandbox_workspace_write.network_access or null) == false
            && hasInfix (escapeShellArg (builtins.toJSON cfg.sandbox)) config.home.activation.codexSandbox.data;
          message = "wgn.home.codex: interactive Codex must run in workspace-write with network_access = false, written into config.toml";
        }
      ];

      home.activation.codexSandbox = lib.hm.dag.entryAfter ["writeBoundary"] ''
        if [ -z "''${DRY_RUN:-}" ]; then
          ${setKeys} "$HOME/.codex/config.toml" ${escapeShellArg (builtins.toJSON cfg.sandbox)} \
            || warnEcho "Codex's sandbox was not pinned to workspace-write without network."
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
