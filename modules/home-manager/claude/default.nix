{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.claude;
in {
  options.wgn.home.claude = {
    enable = mkEnableOption "Enables my Claude Code setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs.claude-code = {
      enable = true;

      context = ./CLAUDE.md;

      skills = {
        restack-branches = ./skills/restack-branches;
      };

      # Referencing gopls by store path keeps this working without the Go
      # module, which is not enabled everywhere Claude Code is.
      lspServers = {
        go = {
          command = "${pkgs.gopls}/bin/gopls";
          args = ["serve"];

          extensionToLanguage = {
            ".go" = "go";
          };
        };
      };

      # settings.json is a read-only symlink into the store, so preferences
      # that Claude Code would otherwise write there itself have to be set
      # here to survive.
      settings = {
        model = "opus[1m]";
        theme = "auto";
        inputNeededNotifEnabled = true;
        agentPushNotifEnabled = true;
      };
    };
  };
}
