{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.photos;
in {
  options.wgn.home.photos = {
    enable = mkEnableOption "Enables my photo setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      darktable
      gimp-with-plugins
    ];
  };
}
