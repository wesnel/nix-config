{
  config,
  lib,
  pkgs,
  key,
  ...
}:
with lib; let
  cfg = config.wgn.home.git;
in {
  options.wgn.home.git = {
    enable = mkEnableOption "Enables my Git setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      gh
    ];

    sops = {
      secrets = {
        email = {};
        name = {};
      };

      templates = {
        "git.inc" = {
          content = ''
            [user]
              email = "${config.sops.placeholder.email}"
              name = "${config.sops.placeholder.name}"
          '';
        };
      };
    };

    programs.delta = {
      enable = true;
      enableGitIntegration = true;
    };

    programs.git = {
      enable = true;
      lfs.enable = true;

      includes =
        [
          {
            path = config.sops.templates."git.inc".path;
          }
          # TODO: Add work-specific git config using includes.*.condition
        ]
        # These repositories are mirrored on sourcehut, which is the canonical
        # public copy of emacs-config, so a push to origin goes to both. A
        # push URL replaces the fetch URL for pushing, which is why GitHub is
        # listed as well.
        ++ map (repo: {
          condition = "gitdir:~/git/github.com/wesnel/${repo}/";
          contents.remote.origin.pushurl = [
            "git@github.com:wesnel/${repo}.git"
            "git@git.sr.ht:~wgn/${repo}"
          ];
        }) ["nix-config" "emacs-config"];

      settings = {
        commit = {
          gpgsign = true;
        };

        init = {
          defaultBranch = "main";
        };

        tag = {
          gpgsign = true;
        };

        user = {
          signingkey = key;
        };
      };
    };
  };
}
