{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.gamedev;
in {
  options.wgn.home.gamedev = {
    enable = mkEnableOption "Enables my Gamedev setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      godot_4
    ];
  };
}
