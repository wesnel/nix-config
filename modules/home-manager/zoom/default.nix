{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.zoom;
in {
  options.wgn.home.zoom = {
    enable = mkEnableOption "Enables my Zoom setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      zoom-us
    ];
  };
}
