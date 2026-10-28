{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.codex;
  profileName = (builtins.fromJSON (builtins.readFile ./profiles.json)).base;
  cacheRoot = "${config.xdg.cacheHome}/codex/sandbox";
  networkProxy = {
    enabled = true;
    allow_local_binding = true;
    allow_upstream_proxy = false;
    dangerously_allow_all_unix_sockets = false;
    dangerously_allow_non_loopback_proxy = false;
    domains = {
      localhost = "allow";
      "127.0.0.1" = "allow";
      "::1" = "allow";
    };
    unix_sockets."/nix/var/nix/daemon-socket/socket" = "allow";
  };

  llm = import ../llm/lib.nix {inherit lib;};

  mcpProxy = config.wgn.home.mcp-proxy;
  gitSetup = pkgs.callPackage ./git-setup.nix {};

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

  # Reconciles one managed writable root, keeping the rest of the file and
  # any roots set from inside Codex. The third argument records the root added last time,
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

    profiles = doc.setdefault("permissions", tomlkit.table())
    profile = profiles.setdefault("${profileName}", tomlkit.table())
    roots = profile.setdefault("workspace_roots", tomlkit.table())
    changed = False
    if previous and previous != root:
        if previous in roots:
            del roots[previous]
            changed = True
        legacy = doc.get("sandbox_workspace_write", {}).get(
            "writable_roots", [])
        for i in reversed(range(len(legacy))):
            if str(legacy[i]) == previous:
                del legacy[i]
                changed = True
    if roots.get(root) is not True:
        roots[root] = True
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

    managed_profile = keys.get("default_permissions")
    changed = False
    if managed_profile:
        profiles = doc.setdefault("permissions", tomlkit.table())
        profile = profiles.setdefault(managed_profile, tomlkit.table())
        selected = (
            "sandbox_mode" not in doc
            and doc.get("default_permissions") == managed_profile
            and profile.get("extends") == ":workspace"
        )
        if not selected:
            profile.pop("workspace_roots", None)
            profile.pop("filesystem", None)
        roots = profile.setdefault("workspace_roots", tomlkit.table())
        legacy = (
            doc.get("sandbox_workspace_write", {})
            if doc.get("sandbox_mode") == "workspace-write" else {}
        )
        for root in legacy.get("writable_roots", []):
            if str(root) not in roots:
                roots[str(root)] = True
                changed = True
        exclusions = {"exclude_tmpdir_env_var": ":tmpdir",
                      "exclude_slash_tmp": ":slash_tmp"}
        for option, token in exclusions.items():
            if legacy.get(option):
                filesystem = profile.setdefault("filesystem", tomlkit.table())
                filesystem[token] = "read"
        for key in ("sandbox_mode", "sandbox_workspace_write"):
            if key in doc:
                del doc[key]
                changed = True


    def restrictions(values):
        result = {}
        for key, value in values.items():
            if isinstance(value, dict):
                nested = restrictions(value)
                if nested:
                    result[key] = nested
            elif value in ("read", "deny"):
                result[key] = value
        return result


    def merge(table, values, path=()):
        changed = False
        for key, value in values.items():
            child_path = path + (key,)
            if child_path == ("permissions", managed_profile):
                previous = table.get(key, {})
                value = dict(value)
                roots = dict(previous.get("workspace_roots", {}))
                roots.update(value.get("workspace_roots", {}))
                if roots:
                    value["workspace_roots"] = roots
                limited = restrictions(previous.get("filesystem", {}))
                if limited:
                    value["filesystem"] = limited
            # Managed policies replace saved grants; environment keys merge.
            if (isinstance(value, dict)
                    and child_path not in (("features", "network_proxy"),
                                           ("permissions", managed_profile))):
                child = table.setdefault(key, tomlkit.table())
                changed = merge(child, value, child_path) or changed
            elif table.get(key) != value:
                table[key] = value
                changed = True
        return changed


    if merge(doc, keys) or changed:
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
        default_permissions = profileName;
        permissions.${profileName} = {
          extends = ":workspace";
          network.enabled = true;
          workspace_roots = genAttrs [cacheRoot plans.dir] (_: true);
        };
        features.network_proxy = networkProxy;
        shell_environment_policy.set = {
          GOCACHE = "${cacheRoot}/go-build";
          GOLANGCI_LINT_CACHE = "${cacheRoot}/golangci-lint";
          XDG_CACHE_HOME = "${cacheRoot}/xdg";
        };
      };

      description = ''
        The sandbox every interactive Codex session's commands run in,
        merged into ~/.codex/config.toml on every switch, so that it never
        rests on what Codex chooses for a trusted project. An assertion
        holds it to the workspace profile with a proxy permitting host loopback
        and the Nix daemon socket. The daemon builds outside this sandbox.
      '';
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      wgn.home.llm.enable = true;
      home.packages = [gitSetup];

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

    # The proxy allows host loopback and only the Nix daemon's Unix socket.
    # Nix evaluates in the client sandbox, but daemon builds run outside it
    # with the daemon's filesystem and network authority. Git delegate's
    # `codex exec --ignore-user-config` does not read these settings.
    {
      assertions = [
        {
          assertion =
            (cfg.sandbox.default_permissions or null)
            == profileName
            && (cfg.sandbox.permissions.${profileName}.extends or null) == ":workspace"
            && (cfg.sandbox.permissions.${profileName}.network.enabled or null) == true
            && !(cfg.sandbox ? sandbox_mode)
            && !(cfg.sandbox ? sandbox_workspace_write)
            && (cfg.sandbox.features.network_proxy or null) == networkProxy
            && hasInfix (escapeShellArg (builtins.toJSON cfg.sandbox)) config.home.activation.codexSandbox.data;
          message = "wgn.home.codex: interactive Codex must use the managed workspace profile and local network proxy, written into config.toml";
        }
      ];

      home.activation.codexSandbox = lib.hm.dag.entryAfter ["writeBoundary"] ''
        if [ -z "''${DRY_RUN:-}" ]; then
          ${setKeys} "$HOME/.codex/config.toml" ${escapeShellArg (builtins.toJSON cfg.sandbox)} \
            || warnEcho "Codex's sandbox and local network proxy settings were not written."
          mkdir -p ${escapeShellArg cacheRoot}
          ${addWritableRoot} "$HOME/.codex/config.toml" ${escapeShellArg cacheRoot} \
            ${escapeShellArg "${config.xdg.stateHome}/codex/cache-root"} \
            || warnEcho "Codex's tool cache was not made writable in its sandbox."
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
