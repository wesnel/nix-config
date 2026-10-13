{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.codex;

  llm = import ../llm/lib.nix {inherit lib;};

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
    "permissions.pr_loop.network.domains" = genAttrs config.wgn.home.llm.prLoop.allowedDomains (_: "allow");
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

  prLoopLauncher = pkgs.writeShellApplication {
    name = "codex-pr-loop";
    text = ''
      filesystem="${concatStringsSep ", " (
        map (p: ''\"${p}\"=\"deny\"'') config.wgn.home.llm.prLoop.deniedReads
        ++ map (p: ''\"{workspace}/${p}\"=\"read\"'') config.wgn.home.llm.prLoop.protectedPaths
        ++ [''\"${config.wgn.home.llm.prLoop.cacheDir}\"=\"write\"'']
      )}"
      ${config.wgn.home.llm.prLoop.sandboxCaches}
      # The review-loop skill starts only where this is set.
      export PR_LOOP_SESSION=1
      exec ${lib.getExe' pkgs.agent-tools "sandbox-relay"} session -- codex \
        ${concatStringsSep " \\\n  " (mapAttrsToList (k: v: "-c ${escapeShellArg "${k}=${toml v}"}") overrides)} \
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
        llm.mkSkillFiles ".codex/skills" config.wgn.home.llm.skills
        // {
          # Codex skips this when an AGENTS.override.md beside it is non-empty.
          ".codex/AGENTS.md".source = config.wgn.home.llm.instructions;
        };
    }

    (mkIf cfg.prLoop.enable {
      wgn.home.llm.prLoop.enable = true;
      home.packages = [prLoopLauncher];
    })
  ]);
}
