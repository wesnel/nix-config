{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.nix;
in {
  options.wgn.darwin.nix = {
    enable = mkEnableOption "Enables my Nix command setup for Darwin";
  };

  config = mkIf cfg.enable {
    nixpkgs = {
      config = {
        allowUnfree = true;
      };
    };

    nix = {
      settings.experimental-features = "nix-command flakes";
    };
  };
}
