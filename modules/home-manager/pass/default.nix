{ pkgs
, ... }:

{
  imports = [
    ../../../components/pass
  ];

  home.packages = with pkgs; [
    ripasso-cursive
  ];
}
