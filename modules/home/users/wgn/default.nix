inputs@
{ pkgs
, system
, ...
}:

{
  imports = [
    ../../accounts/email/fastmail
    ../../programs/bat
    ../../programs/bash
    ../../programs/direnv
    ../../programs/emacs
    ../../programs/fish
    ../../programs/fzf
    ../../programs/git
    ../../programs/gpg
    ../../programs/helix
    ../../programs/home-manager
    ../../programs/htop
    ../../programs/jq
    ../../programs/kitty
    ../../programs/man
    ../../programs/nix-index
    ../../programs/nvim
    ../../programs/pass
    ../../services/emacs
  ];

  home = {
    stateVersion = "22.05";

    packages = with pkgs; [
      cmake
      exa
      fd
      gcc
      gnumake
      ispell
      libtool
      moreutils
      ripgrep
      texlive.combined.scheme-full
    ];
  };
}
