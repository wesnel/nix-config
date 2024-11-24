{
  emacs-config,
  nixos-hardware,
}: let
  computerName = "orb";
  username = "wesleynelson";
  homeDirectory = "/home/${username}";
  system = "x86_64-linux";

  extraHomeManagerModules = [
    emacs-config.homeManagerModules.default

    (_: {
      wgn.home = {
        emacs.enable = true;
        fish.enable = true;
        git.enable = true;
        gnupg.enable = true;
        man.enable = true;
        pass.enable = true;
        yubikey.enable = true;
      };
    })
  ];

  extraNixOSModules = [
    emacs-config.nixosModules.default

    (_: {
      wgn.nixos = {
        emacs.enable = true;
        fish.enable = true;
        fonts.enable = true;
        nix.enable = true;
        users.enable = true;
        yubikey.enable = true;
      };
    })

    /etc/nixos/configuration.nix
  ];
in {
  inherit
    computerName
    username
    homeDirectory
    system
    extraHomeManagerModules
    extraNixOSModules
    ;
}
