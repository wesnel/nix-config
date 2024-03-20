{ pkgs
, ... }:

{
  fonts = {
    packages = with pkgs; [
      inter
      inter-ui
      nerdfonts
      unifont
    ];
  };
}
