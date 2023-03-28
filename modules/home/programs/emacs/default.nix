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
          rev = "f9bc050123e9fbf600a248c53f3dc2d75544af63";
          sha256 = "sha256-oosvKPfrfPU3sqYSvImBgjhanYL/NyNsuATH02YjfEE=";
        };

        recursive = true;
      };
    };
  };
}
