{
  config,
  lib,
  pkgs,
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

        # Ghostty is not packaged for Darwin in nixpkgs; on macOS the app
        # is expected to be installed out-of-band (e.g. via Homebrew) and
        # home-manager only manages the config file.
        package = mkDefault (
          if pkgs.stdenv.isDarwin
          then null
          else pkgs.ghostty
        );

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
