inputs@
{ pkgs
, ...
}:

{
  fonts = {
    fonts = with pkgs; [
      nerdfonts
    ];
  };
}
