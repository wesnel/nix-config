{emacs-config}: let
  computerName = "devbox";
  username = "wesley";
  homeDirectory = "/home/${username}";
  system = "x86_64-linux";

  deploy = {
    # Resolved through the `Host devbox` block that the ngrok machine writes
    # into ~/.ssh/config, which keeps the real hostname in secrets/wgn.yaml.
    hostname = "devbox";

    # This machine is deployed from an aarch64-darwin one, which cannot build
    # the x86_64-linux closure it is pushing.
    remoteBuild = true;
  };

  extraHomeManagerModules = [
    emacs-config.homeManagerModules.default

    (_: {
      wgn.home = {
        amp.enable = true;
        bash.enable = true;
        claude.enable = true;
        codex.enable = true;
        eca = {
          enable = true;
        };
        emacs.enable = true;
        fish.enable = true;
        git.enable = true;
        gnupg.enable = true;
        helix.enable = true;
        llm.digests.enable = true;
        llm.repos = {
          "ngrok-private/ngrok-agent".setup = "go tool buf generate";
          "wesnel/nix-config" = {
            moduleDir = "overlays/agent-tools";
            test = "cd overlays/agent-tools && go test -count=1 ./...";
            build = "cd overlays/agent-tools && go vet ./...";
            testFiles = ["overlays/agent-tools/**/*_test.go" "overlays/agent-tools/**/testdata/**"];
          };
        };
        man.enable = true;
        mcp-proxy.enable = true;
        mosh.enable = true;
        zellij.enable = true;
      };

      home.programs.wgn.emacs = {
        eca = {
          enable = true;
          sandbox = {
            enable = true;
            backend = "bubblewrap";
          };
        };
        claude.enable = true;
        codex.enable = true;
      };
    })

    ({
      config,
      lib,
      ...
    }: {
      # The GnuPG and SSH agent sockets under /run/user/1000/gnupg are
      # RemoteForward'd here from the ngrok machine, which holds the
      # YubiKey. Anything that starts a local gpg-agent binds those paths
      # first and breaks the forward, so the local agent stays off and the
      # distro's socket units are masked on every activation.
      programs.bash.initExtra = ''
        export SSH_AUTH_SOCK=/run/user/1000/gnupg/S.gpg-agent.ssh
      '';

      home = {
        stateVersion = "25.11";

        # Ubuntu ships its own ~/.bashrc and ~/.profile, which home-manager
        # replaces; without this, activation refuses to overwrite them.
        backupFileExtension = "backup";

        # The activation PATH carries only the store paths home-manager puts
        # there, so systemctl has to be named in full or the loop silently
        # does nothing.
        activation.maskGpgAgentSockets = lib.hm.dag.entryAfter ["writeBoundary"] ''
          for unit in gpg-agent.socket gpg-agent-ssh.socket gpg-agent-extra.socket gpg-agent-browser.socket; do
            $DRY_RUN_CMD ${config.systemd.user.systemctlPath} --user mask --now "$unit"
          done
        '';
      };

      services.gpg-agent.enable = lib.mkForce false;
    })

    ({
      config,
      lib,
      pkgs,
      ...
    }: {
      # This machine belongs to an employer, so it holds no key of its own in
      # .sops.yaml: secrets are decrypted with the PGP key on the YubiKey,
      # reached through the forwarded agent. The default GnuPG home resolves
      # to the forwarded socket directory, and each decryption needs a touch
      # on ngrok. Secrets live in /run/user, so after a reboot they are back
      # only once ngrok's forward is.
      sops = {
        age = {
          keyFile = lib.mkForce null;
          generateKey = lib.mkForce false;
        };

        gnupg = {
          home = "${homeDirectory}/.gnupg";
        };

        # Without the forward, gpg would start a local agent on the forwarded
        # socket's path, which stops the forward binding.
        environment.SOPS_GPG_EXEC = "${pkgs.writeShellScript "gpg-no-autostart" ''
          exec ${config.sops.gnupg.package}/bin/gpg --no-autostart "$@"
        ''}";
      };

      systemd.user = {
        # Setting sops.gnupg.home makes sops-nix wait for
        # graphical-session-pre.target, which never activates on this headless
        # machine, so secrets would only exist until the next reboot.
        services.sops-nix = {
          Install.WantedBy = lib.mkForce ["default.target"];
          # Stays active after decrypting, or the path unit below would start
          # it again while the socket exists, asking for a touch each time.
          Service.RemainAfterExit = true;
          # The path unit also starts it again after a failure. A missed
          # touch takes long enough that systemd's default start limit never
          # trips, so without this it would ask for touches indefinitely.
          Unit = {
            StartLimitIntervalSec = 600;
            StartLimitBurst = 3;
          };
        };

        # Decrypts again when ngrok's forward appears, for a boot that came
        # before it. After three failures in ten minutes it stops; then
        # `systemctl --user reset-failed sops-nix` and
        # `systemctl --user restart sops-nix sops-nix.path`.
        paths.sops-nix = {
          Unit.Description = "Decrypt secrets once the YubiKey is forwarded";
          Path.PathExists = "/run/user/1000/gnupg/S.gpg-agent";
          Install.WantedBy = ["default.target"];
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
    deploy
    extraHomeManagerModules
    ;
}
