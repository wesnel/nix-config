inputs@
{ pkgs
, ...
}:

{
  fonts = {
    fonts = with pkgs; [
      emacs-all-the-icons-fonts
      nerdfonts
    ];
  };
}
