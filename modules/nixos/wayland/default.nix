{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.wayland;
in {
  options.wgn.nixos.wayland = {
    enable = mkEnableOption "Enables my Wayland setup for NixOS";
  };

  config = mkIf cfg.enable {
    security.pam.services.swaylock = {};

    services.displayManager.sessionPackages = with pkgs; [
      sway
    ];

    # other wayland things are managed by home-manager.
  };
}
