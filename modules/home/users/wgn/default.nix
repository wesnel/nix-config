inputs@
{ pkgs
, system
, ...
}:

{
  imports = [
    ../../accounts/email/fastmail
    ../../programs/bat
    ../../programs/direnv
    ../../programs/emacs
    ../../programs/fzf
    ../../programs/git
    ../../programs/gpg
    ../../programs/home-manager
    ../../programs/htop
    ../../programs/jq
    ../../programs/kitty
    ../../programs/man
    ../../programs/nix-index
    ../../programs/nvim
    ../../programs/pass
    ../../services/gpg-agent
    ../../services/lorri
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
    ];
  };
}
