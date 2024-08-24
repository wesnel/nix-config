{ pkgs
, ... }:

{
  home.packages = with pkgs; [
    audacity
    orca-c
    yoshimi
  ];
}
