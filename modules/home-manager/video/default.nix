{ pkgs
, ... }:

{
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
}
