inputs@
{ lib
, pkgs
, config
, ...
}:

let
  package = pkgs.emacsUnstable;
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

    packages = with pkgs.emacsPackages; [
      vterm
    ];
  };

  # FIXME: make configurable with conflicting helix options
  # home.sessionVariables.EDITOR = "emacsclient -t --alternate-editor=''";
  # programs.fish.interactiveShellInit = "set -gx EDITOR emacsclient -t --alternate-editor=''";
}
