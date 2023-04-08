inputs@
  { lib
  , pkgs
  , config
  , ... }:

{
  programs.emacs = {
    enable = true;
    package = pkgs.emacs-config;
  };

  programs.fish.interactiveShellInit = "set -gx EDITOR emacsclient -t --alternate-editor=''";
}
