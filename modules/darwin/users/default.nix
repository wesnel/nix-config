{
  config,
  lib,
  username,
  homeDirectory,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.users;
in {
  options.wgn.darwin.users = {
    enable = mkEnableOption "Enables my user setup for Darwin";
  };

  config = mkIf cfg.enable {
    users = {
      users.${username} = {
        home = homeDirectory;
      };
    };
  };
}
