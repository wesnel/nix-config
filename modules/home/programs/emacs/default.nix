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

    overrides = final: prev: {
      chatgpt-shell = pkgs.callPackage ./overrides/chatgpt-shell {
        inherit (pkgs) fetchFromGitHub;
        inherit (prev) trivialBuild markdown-mode;
      };
    };

    extraPackages = epkgs: with epkgs; [
      chatgpt-shell
    ];
  };

  home = {
    file = {
      ".emacs.d" = {
        source = pkgs.fetchFromGitHub {
          owner = "wesnel";
          repo = "prelude";
          rev = "5d2d01c3aed8ebf12353634c6645c7e142ba183c";
          sha256 = "sha256-JfghBx9RpFawv8tQ9vcQuPDLhkLCvThH/FtKxpJ+WVc=";
        };

        recursive = true;
      };
    };
  };
}
