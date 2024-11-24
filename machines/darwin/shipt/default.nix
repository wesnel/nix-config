{emacs-config}: let
  computerName = "shipt";
  username = "wesleynelson";
  homeDirectory = "/Users/${username}";
  system = "x86_64-darwin";

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

  extraDarwinModules = [
    emacs-config.nixosModules.default

    (_: {
      wgn.darwin = {
        defaults.enable = true;
        emacs.enable = true;
        fish.enable = true;
        fonts.enable = true;
        gnupg.enable = true;
        networking.enable = true;
        nix.enable = true;
        paths.enable = true;
        users.enable = true;
        yubikey.enable = true;
      };
    })
  ];
in {
  inherit
    computerName
    username
    homeDirectory
    system
    extraHomeManagerModules
    extraDarwinModules
    ;
}
