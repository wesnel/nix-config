{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.codex;
  sandboxPolicy = config.wgn.home.llm.sandbox;
  profileName = (builtins.fromJSON (builtins.readFile ./profiles.json)).base;
  filesystem =
    genAttrs sandboxPolicy.deniedReads (_: "deny")
    // {
      ":workspace_roots" = genAttrs (map (name: "**/${name}") sandboxPolicy.workspaceDeniedFiles) (_: "deny");
    };
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

  reconcile =
    pkgs.writers.writePython3 "codex-reconcile" {
      libraries = [pkgs.python3Packages.tomlkit];
    } ''
      import runpy
      import sys

      sys.path.insert(0, "${./.}")
    runpy.run_path(
        "${./.}/reconcile.py",
        run_name="__main__",
    )
    '';
  plans = config.wgn.home.llm.plans;
  managedRoots = {
    plans = plans.dir;
    cache = cacheRoot;
  };
  managedSettings = recursiveUpdate cfg.sandbox (
    {notice.hide_rate_limit_model_nudge = true;}
    // optionalAttrs mcpProxy.enable {
      mcp_servers =
        mapAttrs (_: path: {
          url = mcpProxy.url + path;
          http_headers_helper = mcpProxy.headersHelper;
        })
        mcpProxy.endpoints;
    }
  );
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
          inherit filesystem;
          network = networkProxy;
        };
        features.network_proxy = true;
        shell_environment_policy = {
          ignore_default_excludes = false;
          filters = genAttrs sandboxPolicy.deniedEnvVars (_: "exclude");
          set = {
            GOCACHE = "${cacheRoot}/go-build";
            GOLANGCI_LINT_CACHE = "${cacheRoot}/golangci-lint";
            XDG_CACHE_HOME = "${cacheRoot}/xdg";
          };
        };
      };

      description = ''
        The default permission profile written into ~/.codex/config.toml
        on each switch. It extends workspace protections, denies shared
        credential paths and uses a proxy permitting host loopback and the
        Nix daemon socket. Higher-precedence settings can override this
        user configuration; daemon builds run outside its sandbox.
      '';
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      wgn.home.llm.enable = true;
      wgn.home.llm.prLoop.enable = mkDefault true;
      home.packages = [gitSetup];

      # Codex is installed and updated by itself, and writes ~/.codex/config.toml
      # to record project trust, MCP servers and model choice. Generating that
      # file is what stops it being able to.
      home.file =
        llm.mkSkillFiles pkgs ".codex/skills" config.wgn.home.llm.skills
        // {
          # Codex skips this when an AGENTS.override.md beside it is non-empty.
          ".codex/AGENTS.md".source = config.wgn.home.llm.instructions;

          # Adds the baseline guard to supported tool paths. Codex skips a
          # hook until it is trusted with /hooks, and trust follows the hook's
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
                matcher = "compact";
                hooks = [
                  {
                    type = "command";
                    command = "agent-plan remind";
                  }
                ];
              }
              {
                matcher = "startup|resume|clear|fork";
                hooks = [
                  {
                    type = "command";
                    command = "agent-plan find --hook --harness codex";
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
            && !(cfg.sandbox ? sandbox_mode)
            && !(cfg.sandbox ? sandbox_workspace_write)
            && (cfg.sandbox.permissions.${profileName}.extends or null) == ":workspace"
            && (cfg.sandbox.permissions.${profileName}.filesystem or null) == filesystem
            && (cfg.sandbox.permissions.${profileName}.network or null) == networkProxy
            && (cfg.sandbox.features.network_proxy or null) == true
            && (cfg.sandbox.shell_environment_policy.ignore_default_excludes or null) == false
            && (cfg.sandbox.shell_environment_policy.filters or null) == genAttrs sandboxPolicy.deniedEnvVars (_: "exclude")
            && hasInfix (escapeShellArg (builtins.toJSON managedSettings)) config.home.activation.codexSandbox.data;
          message = "wgn.home.codex: the default workspace profile must deny shared credentials and use the loopback and Nix-socket proxy through one configuration reconciliation";
        }
      ];

      # One operation migrates legacy keys, roots, notices and MCP entries.
      # Plans remain writable to every sandboxed command; the guard's plan
      # rules distinguish agent-plan writes from other shell and file tools.
      home.activation.codexSandbox = lib.hm.dag.entryAfter (["writeBoundary"] ++ optional mcpProxy.enable "mcpProxyToken") ''
        if [ -z "''${DRY_RUN:-}" ]; then
          if ${reconcile} "$HOME/.codex/config.toml" ${escapeShellArg (builtins.toJSON managedSettings)} \
            ${escapeShellArg (builtins.toJSON managedRoots)} ${escapeShellArg "${config.xdg.stateHome}/codex"}; then
            mkdir -p ${escapeShellArg cacheRoot}
          else
            warnEcho "Codex configuration was not reconciled; resolve the reported conflict before starting a session."
          fi
        fi
      '';
    }
  ]);
}
