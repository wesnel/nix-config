{
  config,
  emacs-skills,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.llm;
  docs = config.wgn.home.docs-mcp-server;
in {
  options.wgn.home.llm = {
    enable = mkEnableOption "Enables the plumbing shared by my coding agents";

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
                  File linked in as the skill. Mutually exclusive with
                  {option}`text`.
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

              sandbox = mkOption {
                type = bool;
                default = false;

                description = ''
                  Whether the skill works with nothing but the workspace and
                  the sandbox's own tools.

                  Off by default, because most of these drive something on
                  this machine -- an editor, a notifier, a command installed
                  here -- and none of it exists in the guest. Such a skill is
                  worse there than missing: it is offered to the agent, which
                  spends a turn calling it and gets nothing back.
                '';
              };
            };
          })
        );

      description = ''
        Skills offered to every enabled coding agent. A path is linked into
        the agent's skill directory; a string is written there verbatim.

        Give {option}`sandbox` instead of a bare path or string to say the
        skill may also be carried into a sandboxed session.
      '';

      default =
        {
          mcp-cli = ./skills/mcp-cli/SKILL.md;
          notify = ./skills/notify/SKILL.md;

          describe = builtins.readFile "${emacs-skills}/skills/describe/SKILL.md";
          dired = builtins.readFile "${emacs-skills}/skills/dired/SKILL.md";
          emacsclient = builtins.readFile "${emacs-skills}/skills/emacsclient/SKILL.md";
          file-links = builtins.readFile "${emacs-skills}/skills/file-links/SKILL.md";
          highlight = builtins.readFile "${emacs-skills}/skills/highlight/SKILL.md";
          open = builtins.readFile "${emacs-skills}/skills/open/SKILL.md";
          select = builtins.readFile "${emacs-skills}/skills/select/SKILL.md";
        }
        // optionalAttrs pkgs.stdenv.hostPlatform.isDarwin {
          trash = ./skills/trash/SKILL.md;
        };
    };
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      mcp-cli

      # mcp-cli shells out to npx for servers distributed on npm.
      nodejs
    ];

    # This writes only ~/.config/mcp/mcp.json, which mcp-cli reads and no
    # agent writes to. Each agent is told about these servers through its own
    # `mcp add`, so that none of their settings files have to be generated.
    programs.mcp = {
      enable = true;

      servers = {
        # Started per client unless a service already runs it, in which case
        # every client reaches that one and shares the index rather than
        # building a private one and contending for the same database.
        docs-mcp-server =
          if docs.enable
          then {
            type = "http";
            url = "http://127.0.0.1:${toString docs.port}/mcp";
          }
          else {
            type = "stdio";
            command = "${pkgs.nodejs}/bin/npx";

            args = [
              "-y"
              "@arabold/docs-mcp-server@latest"
            ];

            env = {
              DOCS_MCP_TELEMETRY = "false";
            };
          };
      };
    };

    # Some clients look for the older file name, which programs.mcp does not
    # write.
    xdg.configFile."mcp/mcp_servers.json" = {
      enable = true;
      source = config.xdg.configFile."mcp/mcp.json".source;
    };
  };
}
