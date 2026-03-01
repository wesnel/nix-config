{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.teams;
in {
  options.wgn.home.teams = {
    enable = mkEnableOption "Enables my Teams setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      teams
    ];
  };
}
