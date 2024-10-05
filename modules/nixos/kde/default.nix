{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.kde;
in {
  options.wgn.nixos.kde = {
    enable = mkEnableOption "Enables my KDE setup for NixOS";
  };

  config = mkIf cfg.enable {
    services = {
      desktopManager = {
        plasma6.enable = true;
      };

      displayManager = {
        defaultSession = "plasma";
      };
    };

    programs.dconf.enable = true;
  };
}
