{ pkgs
, ... }:

{
  home.packages = with pkgs; [
    filmulator-gui
  ];
}
