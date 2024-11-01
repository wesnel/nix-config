{
  pkgs,
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.emacs;
in {
  options.wgn.nixos.emacs = {
    enable = mkEnableOption "Enables my Emacs setup for NixOS";
  };

  config = mkIf cfg.enable {
    programs = {
      fish.interactiveShellInit =
        lib.mkIf config.programs.fish.enable
        "set -gx EDITOR emacs";

      wgn.emacs = {
        enable = true;
        package = pkgs.emacs;
      };
    };

    services.emacs = {
      enable = true;
    };
  };
}
