{emacs-config}: let
  computerName = "artifact";
  username = "wgn";
  homeDirectory = "/Users/${username}";
  system = "x86_64-darwin";

  extraHomeManagerModules = [
    emacs-config.homeManagerModules.default

    (_: {
      wgn.home = {
        copilot.enable = true;
        emacs.enable = true;
        firefox.enable = true;
        fish.enable = true;
        gamedev.enable = true;
        games.enable = true;
        gcloud.enable = true;
        git.enable = true;
        gnupg.enable = true;
        go.enable = true;
        man.enable = true;
        pass.enable = true;
        python.enable = true;
        yubikey.enable = true;
      };
    })

    (_: {
      sops = {
        defaultSopsFile = ../../../secrets/wgn.yaml;
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
        homebrew.enable = true;
        networking.enable = true;
        nix.enable = true;
        paths.enable = false;
        users.enable = true;
        yubikey.enable = true;
      };
    })

    (_: {
      services = {
        openssh = {
          enable = true;
        };
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
