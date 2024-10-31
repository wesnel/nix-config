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
    programs.git = let
      name = "Wesley Nelson";
      username = "wgn";
      host = "wgn.dev";
    in {
      enable = true;
      userName = name;
      userEmail = "${username}@${host}";
      package = pkgs.gitAndTools.gitFull;
      delta.enable = true;
      lfs.enable = true;

      extraConfig = {
        init = {
          defaultBranch = "main";
        };
      };
    };
  };
}
