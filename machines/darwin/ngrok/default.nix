{emacs-config}: let
  computerName = "ngrok";
  username = "wgn";
  homeDirectory = "/Users/${username}";
  system = "aarch64-darwin";

  extraHomeManagerModules = [
    emacs-config.homeManagerModules.default

    (_: {
      wgn.home = {
        aerospace.enable = true;
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

    ({config, ...}: {
      sops = {
        secrets = {
          devbox-host = {};
          devbox-user = {};
        };

        templates = {
          "ssh.inc" = {
            # NOTE: In order to get RemoteForward to work, first
            # ensure that the user ID matches what's on the remote.
            # Then, ensure that the remote /etc/sshd_config includes:
            #
            #   StreamLocalBindUnlink yes
            #
            # and that afterwards you have called:
            #
            #   sudo systemctl reload sshd
            #
            # Then you will need to manually import your public key on
            # the remote.
            content = ''
              Host devbox devbox-*
                  HostName ${config.sops.placeholder.devbox-host}
                  User ${config.sops.placeholder.devbox-user}
                  ForwardAgent yes
                  AddKeysToAgent yes
                  RemoteForward /run/user/1000/gnupg/S.gpg-agent ${homeDirectory}/.gnupg/S.gpg-agent.extra
            '';
          };
        };
      };

      programs.ssh = {
        enable = true;
        enableDefaultConfig = false;

        includes = [
          "${config.sops.templates."ssh.inc".path}"
        ];
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
