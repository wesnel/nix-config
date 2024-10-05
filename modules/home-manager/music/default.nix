{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.music;
in {
  options.wgn.home.music = {
    enable = mkEnableOption "Enables my music setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      audacity
      orca-c
      yoshimi
    ];
  };
}
