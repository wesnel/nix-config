{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.git;
in {
  options.wgn.home.git = {
    enable = mkEnableOption "Enables my Git setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs.delta = {
      enable = true;
      enableGitIntegration = true;
    };

    programs.git = let
      # TODO: Read these from secrets instead of hard-coding them.
      name = "Wesley Nelson";
      username = "wgn";
      host = "wgn.dev";
    in {
      enable = true;
      lfs.enable = true;

      settings = {
        user = {
          email = "${username}@${host}";

          inherit
            name
            ;
        };

        init = {
          defaultBranch = "main";
        };
      };
    };
  };
}
