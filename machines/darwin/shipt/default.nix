_:

let
  computerName = "shipt";
  username = "wesleynelson";
  homeDirectory = "/Users/${username}";
  system = "x86_64-darwin";

  homeManagerModules = [
    ../../../modules/home-manager/emacs
    ../../../modules/home-manager/email
    ../../../modules/home-manager/fish
    ../../../modules/home-manager/git
    ../../../modules/home-manager/gnupg
    ../../../modules/home-manager/man
    ../../../modules/home-manager/pass
    ../../../modules/home-manager/yubikey
  ];

  darwinModules = [
    ../../../modules/darwin/defaults
    ../../../modules/darwin/emacs
    ../../../modules/darwin/fish
    ../../../modules/darwin/fonts
    ../../../modules/darwin/gnupg
    ../../../modules/darwin/networking
    ../../../modules/darwin/nix
    ../../../modules/darwin/paths
    ../../../modules/darwin/users
    ../../../modules/darwin/yubikey
  ];
in {
  inherit
    computerName
    username
    homeDirectory
    system
    homeManagerModules
    darwinModules;
}
