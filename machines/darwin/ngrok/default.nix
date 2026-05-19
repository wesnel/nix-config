{emacs-config}: let
  computerName = "ngrok";
  username = "wgn";
  homeDirectory = "/Users/${username}";
  system = "aarch64-darwin";

  extraHomeManagerModules = [
    emacs-config.homeManagerModules.default

    (_: {
      wgn.home = {
        emacs.enable = true;
        fish.enable = true;
        git.enable = true;
        gnupg.enable = true;
        go.enable = true;
        man.enable = true;
        pass.enable = true;
        python.enable = true;
        yubikey.enable = true;
      };

      home.programs.wgn.emacs.claude.enable = true;
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
