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
          rev = "6df3f76a09e344435648d129352ef84f98733df6";
          sha256 = "sha256-ax1ZGm2M6nSEDDa5g98gPMkjjxRldv9cRPEPCGQxv/4=";
        };

        recursive = true;
      };
    };
  };

  home.sessionVariables.EDITOR = "emacsclient -t --alternate-editor=''";
  programs.fish.interactiveShellInit = "set -gx EDITOR emacsclient -t --alternate-editor=''";
}
