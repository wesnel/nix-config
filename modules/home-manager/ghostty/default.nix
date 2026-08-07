{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.ghostty;
in {
  options.wgn.home.ghostty = {
    enable = mkEnableOption "Enables my Ghostty setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs = {
      ghostty = {
        enable = true;

        settings = {
          "desktop-notifications" = true;
          "shell-integration" = "detect";
          "shell-integration-features" = "cursor,sudo,title";
          "mouse-hide-while-typing" = true;
          "copy-on-select" = "clipboard";
          "window-save-state" = "always";
          "confirm-close-surface" = false;
        };
      };
    };
  };
}
