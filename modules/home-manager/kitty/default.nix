{ pkgs
, ... }:

{
  programs.kitty = {
    enable = true;
    theme = "Modus Vivendi";

    font = {
      name = "Iosevka Nerd Font Mono";
      package = pkgs.nerdfonts;
      size = 10;
    };
  };

  home.packages = with pkgs; [
    imagemagick
  ];
}
