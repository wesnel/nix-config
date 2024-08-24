{ pkgs
, ... }:

{
  home.packages = with pkgs; [
    darktable
    filmulator-gui
    gimp-with-plugins
  ];
}
