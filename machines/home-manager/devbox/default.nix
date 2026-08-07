{emacs-config}: let
  computerName = "devbox";
  username = "wgn";
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
    ;
}
