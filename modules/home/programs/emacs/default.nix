inputs@
{ lib
, pkgs
, config
, ...
}:

let
  package = pkgs.emacsUnstable;
  emacs = "${package}/bin/emacs";
  emacsBin = "${config.home.homeDirectory}/.emacs.d/bin";
in {
  programs.emacs = {
    enable = true;
    inherit package;
  };

  home = {
    sessionPath = [ emacsBin ];

    file.".doom.d" = {
      source = ./doom;
      target = ".doom.d";
      recursive = true;
    };
  };

  home.sessionVariables.EDITOR = "${emacs} -nw";
  programs.fish.interactiveShellInit = "set -gx EDITOR ${emacs} -nw";
}
