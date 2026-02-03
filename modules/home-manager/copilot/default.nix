{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.copilot;
in {
  options.wgn.home.copilot = {
    enable = mkEnableOption "Enables my AI setup for GitHub Copilot";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      github-copilot-cli
    ];
  };
}
