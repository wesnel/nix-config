{ pkgs
, ... }:

{
  home.packages = with pkgs; [
    orca-c
    yoshimi
  ];
}
