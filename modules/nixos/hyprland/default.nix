{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.hyprland;
in {
  options.wgn.nixos.hyprland = {
    enable = mkEnableOption "Enables my Hyprland setup for NixOS";
  };

  config = mkIf cfg.enable {
    security.pam.services.hyprlock = {};

    programs = {
      wgn.emacs.package = pkgs.wgn-emacs-pgtk;

      hyprland = {
        enable = true;
        withUWSM = true;
      };

      uwsm = {
        enable = true;
        waylandCompositors = {
          hyprland = {
            prettyName = "Hyprland";
            comment = "Hyprland compositor managed by UWSM";
            binPath = "/run/current-system/sw/bin/Hyprland";
          };
        };
      };
    };

    environment.sessionVariables.NIXOS_OZONE_WL = "1";
  };
}
