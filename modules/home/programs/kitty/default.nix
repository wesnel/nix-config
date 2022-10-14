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

    settings = {
      "macos_option_as_alt" = true;
    };

    font = {
      name = "FiraCode Nerd Font";
      package = pkgs.nerdfonts;
      size = 10;
    };
  };

  home.packages = with pkgs; [
    imagemagick
  ];
}
