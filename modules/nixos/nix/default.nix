{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.nix;
in {
  options.wgn.nixos.nix = {
    enable = mkEnableOption "Enables my Nix command setup for NixOS";
  };

  config = mkIf cfg.enable {
    nix = {
      package = pkgs.nixFlakes;

      extraOptions = ''
        experimental-features = nix-command flakes
        show-trace = true
      '';
    };
  };
}
