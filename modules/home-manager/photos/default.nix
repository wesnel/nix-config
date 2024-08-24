{ pkgs
, ... }:

{
  home.packages = with pkgs; [
    filmulator-gui
    gimp-with-plugins
  ];
}
