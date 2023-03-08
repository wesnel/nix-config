inputs@
{ lib
, pkgs
, config
, ...
}:

{
  programs.emacs = {
    enable = true;
    package = pkgs.emacsGit;
  };

  home = {
    file = {
      ".emacs.d" = {
        source = pkgs.fetchFromGitHub {
          owner = "wesnel";
          repo = "prelude";
          rev = "dd8acd814dbcf0d935ec6ce4d51d8f530d4c518c";
          sha256 = "sha256-bgG4uMWlxtYc2jEOUomu32EqUvHCRuh7S6wcswge4W0=";
        };

        recursive = true;
      };
    };
  };

  home.sessionVariables.EDITOR = "emacsclient -t --alternate-editor=''";
  programs.fish.interactiveShellInit = "set -gx EDITOR emacsclient -t --alternate-editor=''";
}
