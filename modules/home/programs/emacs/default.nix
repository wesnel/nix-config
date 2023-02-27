inputs@
{ lib
, pkgs
, config
, ...
}:

let
  emacsBin = "${config.home.homeDirectory}/.emacs.d/bin";
in {
  programs.emacs = {
    enable = true;
    package = pkgs.emacsGit;
  };

  home = {
    packages = with pkgs; [
      emacsPackages.vterm
    ];

    sessionPath = [
      emacsBin
    ];

    file = {
      ".emacs.d" = {
        source = pkgs.fetchFromGitHub {
          owner = "bbatsov";
          repo = "prelude";
          rev = "fcb629acb645cdff7fdd5f7332bb669c75527fdb";
          sha256 = "sha256-Q+MFNEqr6HnyO+gDulVyHjWpVzWhXcv+scCIRMIVm5A=";
        };

        recursive = true;
      };

      ".emacs.d/prelude-modules.el" = {
        source = ./.emacs.d/prelude-modules.el;
      };
    };
  };

  home.sessionVariables.EDITOR = "emacsclient -t --alternate-editor=''";
  programs.fish.interactiveShellInit = "set -gx EDITOR emacsclient -t --alternate-editor=''";
}
