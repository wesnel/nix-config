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
    # swaywm/sway/issues/2773
    security.pam.services.swaylock = {
      text = ''
        auth include login
      '';
    };

    services.displayManager.sessionPackages = with pkgs; [
      sway
    ];

    # other wayland things are managed by home-manager.
  };
}
