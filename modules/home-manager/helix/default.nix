{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.helix;
in {
  options.wgn.home.helix = {
    enable = mkEnableOption "Enables my Helix setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs.helix = {
      enable = true;
    };
  };
}
