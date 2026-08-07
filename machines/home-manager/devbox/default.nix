{emacs-config}: let
  computerName = "devbox";
  username = "wesley";
  homeDirectory = "/home/${username}";
  system = "x86_64-linux";

  extraHomeManagerModules = [
    emacs-config.homeManagerModules.default

    (_: {
      wgn.home = {
        claude.enable = true;
        emacs.enable = true;
        fish.enable = true;
        git.enable = true;
        gnupg.enable = true;
        helix.enable = true;
        man.enable = true;
        mosh.enable = true;
        pass.enable = true;
        zellij.enable = true;
      };

      home.programs.wgn.emacs = {
        claude.enable = true;
        codex.enable = true;
      };
    })

    ({lib, ...}: {
      home = {
        stateVersion = "25.11";

        # The GnuPG and SSH agent sockets under /run/user/1000/gnupg are
        # RemoteForward'd here from the ngrok machine, which holds the
        # YubiKey. Anything that starts a local gpg-agent binds those paths
        # first and breaks the forward, so the local agent stays off and the
        # distro's socket units are masked on every activation.
        file.".bash_aliases".text = ''
          export SSH_AUTH_SOCK=/run/user/1000/gnupg/S.gpg-agent.ssh
        '';

        activation.maskGpgAgentSockets = lib.hm.dag.entryAfter ["writeBoundary"] ''
          for unit in gpg-agent.socket gpg-agent-ssh.socket gpg-agent-extra.socket gpg-agent-browser.socket; do
            $DRY_RUN_CMD systemctl --user mask --now "$unit" 2>/dev/null || true
          done
        '';
      };

      services.gpg-agent.enable = lib.mkForce false;
    })

    ({lib, ...}: {
      # There is no age recipient for this machine in .sops.yaml, so secrets
      # are decrypted with the PGP key on the YubiKey, reached through the
      # forwarded agent. The default GnuPG home resolves to the forwarded
      # socket directory, and each decryption needs a touch on ngrok.
      sops = {
        age = {
          keyFile = lib.mkForce null;
          generateKey = lib.mkForce false;
        };

        gnupg = {
          home = "${homeDirectory}/.gnupg";
        };
      };

      # Setting sops.gnupg.home makes sops-nix wait for
      # graphical-session-pre.target, which never activates on this headless
      # machine, so secrets would only exist until the next reboot.
      systemd.user.services.sops-nix.Install.WantedBy = lib.mkForce ["default.target"];
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
