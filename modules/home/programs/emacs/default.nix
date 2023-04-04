inputs@
{ lib
, pkgs
, config
, ...
}:

{
  programs.emacs = {
    enable = true;

    package = pkgs.emacsWithPackagesFromUsePackage {
      package = pkgs.emacsGit;
      config = ./init.el;
      defaultInitFile = true;
    };

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

  home.packages = with pkgs; [
    (aspellWithDicts (d: with d; [
      en
      en-computers
      en-science
    ]))
  ];

  programs.fish.interactiveShellInit = "set -gx EDITOR emacsclient -t --alternate-editor=''";
}
