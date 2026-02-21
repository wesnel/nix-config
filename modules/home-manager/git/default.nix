{
  config,
  lib,
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

      includes = [
        {
          path = config.sops.templates."git.inc".path;
        }
        # TODO: Add work-specific git config using includes.*.condition
      ];

      settings = {
        init = {
          defaultBranch = "main";
        };
      };
    };
  };
}
