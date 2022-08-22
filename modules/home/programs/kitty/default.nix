inputs@
{ config
, lib
, pkgs
, ...
}:

{
  programs.kitty = {
    enable = true;
    theme = "Gruvbox Dark";

    font = {
      name = "FiraCode Nerd Font";
      package = pkgs.nerdfonts;
      size = 10;
    };
  };
}
