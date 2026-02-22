{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.slack;
in {
  options.wgn.home.slack = {
    enable = mkEnableOption "Enables my Slack setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      slack
    ];
  };
}
