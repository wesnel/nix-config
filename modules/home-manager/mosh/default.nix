{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.mosh;
in {
  options.wgn.home.mosh = {
    enable = mkEnableOption "Enables my Mosh setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      mosh
    ];
  };
}
