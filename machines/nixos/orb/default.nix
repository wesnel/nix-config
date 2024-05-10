{ emacs-config
, nixos-hardware }:

let
  computerName = "orb";
  username = "wesleynelson";
  homeDirectory = "/home/${username}";
  system = "x86_64-linux";

  homeManagerModules = [
    emacs-config.nixosModules.home

    ../../../modules/home-manager/emacs
    ../../../modules/home-manager/email
    ../../../modules/home-manager/fish
    ../../../modules/home-manager/git
    ../../../modules/home-manager/gnupg
    ../../../modules/home-manager/man
    ../../../modules/home-manager/pass
    ../../../modules/home-manager/yubikey
  ];

  nixosModules = [
    emacs-config.nixosModules.nixos

    ../../../modules/nixos/fish
    ../../../modules/nixos/fonts
    ../../../modules/nixos/nix
    ../../../modules/nixos/users

    /etc/nixos/configuration.nix
  ];
in {
  inherit
    computerName
    username
    homeDirectory
    system
    homeManagerModules
    nixosModules;
}
