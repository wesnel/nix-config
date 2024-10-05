{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.video;
in {
  options.wgn.home.video = {
    enable = mkEnableOption "Enables my video setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      blender
      handbrake
    ];

    programs.obs-studio = {
      enable = true;

      plugins = with pkgs.obs-studio-plugins; [
        wlrobs
      ];
    };
  };
}
