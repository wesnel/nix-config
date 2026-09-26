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
        amp.enable = true;
        claude.enable = true;
        codex.enable = true;
        docs-mcp-server.enable = true;
        eca = {
          enable = true;
          localModel = "ollama/qwen2.5-coder:7b";
          sandbox.enable = true;
        };
        emacs.enable = true;
        fish.enable = true;
        fonts.enable = true;
        ghostty.enable = true;
        git.enable = true;
        gnupg.enable = true;
        go.enable = true;
        man.enable = true;
        ollama = {
          enable = true;

          # Matches the limit qwen2.5-coder:7b is given in ECA's own config.
          contextLength = 32768;
        };
        pass.enable = true;
        python.enable = true;
        yubikey.enable = true;
      };

      home.programs.wgn.emacs = {
        claude.enable = true;
        codex.enable = true;
      };
    })

    ({config, ...}: {
      sops = {
        secrets = {
          devbox-host = {};
          devbox-user = {};
        };

        templates = {
          "ssh.inc" = {
            # NOTE: The RemoteForward paths below hardcode UID 1000, which has
            # to be the user's UID on the remote. The remote also needs
            # StreamLocalBindUnlink enabled, or sshd refuses to replace socket
            # files that are already bound and every forward fails; see
            # "replacing the devbox" in the README.
            content = ''
              Host devbox devbox-*
                  HostName ${config.sops.placeholder.devbox-host}
                  User ${config.sops.placeholder.devbox-user}
                  ForwardAgent yes
                  AddKeysToAgent yes
                  ControlMaster auto
                  ControlPath ~/.ssh/control/%r@%h:%p
                  ControlPersist yes

              # Used only by the devbox-agent-forward launchd service to
              # maintain RemoteForward sockets independently of Mosh sessions.
              Host devbox-agent
                  HostName ${config.sops.placeholder.devbox-host}
                  User ${config.sops.placeholder.devbox-user}
                  ControlMaster no
                  ServerAliveInterval 30
                  ServerAliveCountMax 3
                  RemoteForward /run/user/1000/gnupg/S.gpg-agent ${homeDirectory}/.gnupg/S.gpg-agent.extra
                  RemoteForward /run/user/1000/gnupg/S.gpg-agent.ssh ${homeDirectory}/.gnupg/S.gpg-agent.ssh
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

      home.file.".ssh/control/.keep".text = "";
    })

    ({
      config,
      lib,
      ...
    }: {
      sops = {
        secrets = {
          email-work = {};
        };

        templates = {
          "secrets.fish" = {
            content = ''
              set -l __authinfo_elisp '
              (progn
                (require (quote auth-source))
                (require (quote cl-lib))
                (require (quote rx))
                (let* ((specs
                        (quote
                         (("NGROK_AUTHTOKEN_PROD" :host "ngrok.com" :user "${config.sops.placeholder.email-work}")
                          ("NGROK_AUTHTOKEN" :host "ngrok.com.lan" :user "all@example.com"))))
                       (fish-quote
                        (lambda (s)
                          (concat
                           (string #x27)
                           (replace-regexp-in-string
                            (string #x27) (string #x27 #x5c #x27 #x27)
                            (format "%s" s)
                            t t)
                           (string #x27)))))
                  (intern
                   (mapconcat
                    (lambda (spec)
                      (let* ((env (car spec))
                             (query (cdr spec))
                             (auth
                              (car
                               (apply
                                (function auth-source-search)
                                (append query (list :max 1 :require (list :secret))))))
                             (secret (and auth (auth-info-password auth))))
                        (unless (string-match-p
                                 (rx string-start (or alpha "_") (* (or alnum "_")) string-end)
                                 env)
                          (error "Invalid env var name: %s" env))
                        (unless secret
                          (error "No auth-source secret for %s" env))
                        (format "set -gx %s %s" env (funcall fish-quote secret))))
                    specs
                    "; "))))
              '

              set -l __authinfo_fish (
                  emacsclient \
                      --socket-name="$HOME/.emacs.d/var/server/socket/server" \
                      --alternate-editor=false \
                      --eval "$__authinfo_elisp" \
                      2>/dev/null
              )
              set -l __authinfo_status $status

              if test $__authinfo_status -eq 0; and test -n "$__authinfo_fish"; and test "$__authinfo_fish" != nil
                  set -l __authinfo_script (string unescape --style=script -- "$__authinfo_fish")
                  eval "$__authinfo_script"
              end

              set -e __authinfo_elisp __authinfo_fish __authinfo_status __authinfo_script
            '';
          };
        };
      };

      programs.fish.interactiveShellInit = lib.mkAfter ''
        source ${config.sops.templates."secrets.fish".path}
      '';
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

    (_: {
      homebrew.casks = [
        "ghostty"
      ];
    })

    (_: {
      launchd.user.agents.devbox-agent-forward = {
        serviceConfig = {
          ProgramArguments = ["/usr/bin/ssh" "-N" "devbox-agent"];
          EnvironmentVariables = {
            SSH_AUTH_SOCK = "${homeDirectory}/.gnupg/S.gpg-agent.ssh";
          };
          KeepAlive = true;
          ThrottleInterval = 30;
          StandardErrorPath = "/tmp/devbox-agent-forward.log";
          StandardOutPath = "/tmp/devbox-agent-forward.log";
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
