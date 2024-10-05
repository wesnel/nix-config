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
    nix = {
      package = pkgs.nixFlakes;

      extraOptions = ''
        experimental-features = nix-command flakes
        show-trace = true
      '';
    };

    services = {
      nix-daemon = {
        enable = true;
      };
    };
  };
}
