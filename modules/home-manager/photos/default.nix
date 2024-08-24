{ pkgs
, ... }:

{
  home.packages = with pkgs; [
    ansel
    filmulator-gui
    gimp-with-plugins
  ];
}
