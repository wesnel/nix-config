{ pkgs
, ... }:

{
  fonts = {
    fonts = with pkgs; [
      inter
      inter-ui
      nerdfonts
      unifont
    ];
  };
}
