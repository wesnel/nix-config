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

      ".emacs.d/personal/config-go-mode.el".source = ./.emacs.d/personal/config-go-mode.el;
      ".emacs.d/personal/config-nix-mode.el".source = ./.emacs.d/personal/config-nix-mode.el;
      ".emacs.d/personal/prelude-modules.el".source = ./.emacs.d/personal/prelude-modules.el;
    };
  };

  home.sessionVariables.EDITOR = "emacsclient -t --alternate-editor=''";
  programs.fish.interactiveShellInit = "set -gx EDITOR emacsclient -t --alternate-editor=''";
}
