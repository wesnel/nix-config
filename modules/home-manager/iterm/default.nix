{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.iterm;
in {
  options.wgn.home.iterm = {
    enable = mkEnableOption "Enables my iTerm setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      iterm2
    ];
  };
}
