{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.claude;

  llm = import ../llm/lib.nix {inherit lib;};

  # Loaded only by claude-pr-loop, so the guard hooks never apply to an
  # ordinary session. The review-loop skill is shared with the other agents.
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
          matcher = "Bash|mcp__.*";
          hooks = [
            {
              type = "command";
              command = "${lib.getExe' pkgs.agent-tools "pr-loop"} guard";
            }
          ];
        }
      ];
    })} $out/hooks/hooks.json
  '';

  darwin = pkgs.stdenv.hostPlatform.isDarwin;

  # The session's commands run in Claude Code's sandbox with no credentials and
  # no route to GitHub; `sandbox-relay session` serves `pr-loop` on a private
  # socket, whose path replaces {socket}. Hooks, MCP servers and WebFetch run outside
  # the sandbox, which is why WebFetch to GitHub is denied as well.
  prLoopSettings = builtins.toJSON {
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

      network =
        {
          allowedDomains = config.wgn.home.llm.prLoop.allowedDomains;
          strictAllowlist = true;
        }
        # macOS can allow one socket. Linux allows every socket, so the agent
        # sockets are hidden below instead.
        // (
          if darwin
          then {allowUnixSockets = ["{socket}"];}
          else {allowAllUnixSockets = true;}
        );

      filesystem.denyRead = config.wgn.home.llm.prLoop.deniedReads;

      # Relative paths here are not resolved against the workspace, so
      # `sandbox-relay session` fills in its real path.
      filesystem.denyWrite = map (p: "{workspace}/${p}") config.wgn.home.llm.prLoop.protectedPaths;

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
        "This is an unattended review-loop session over draft pull requests. Its only route to GitHub is the `pr-loop` command, which enforces the loop's policy."
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
      exec ${lib.getExe' pkgs.agent-tools "sandbox-relay"} session -- claude \
        --plugin-dir ${prLoopPlugin} \
        --settings ${lib.escapeShellArg prLoopSettings} \
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
        llm.mkSkillFiles ".claude/skills" config.wgn.home.llm.skills
        // {
          ".claude/CLAUDE.md".source = config.wgn.home.llm.instructions;
        };

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
  ]);
}
