{
  config,
  lib,
  username,
  homeDirectory,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.users;
in {
  options.wgn.nixos.users = {
    enable = mkEnableOption "Enables my users setup for NixOS";
  };

  config = mkIf cfg.enable {
    users = {
      users.${username} = {
        isNormalUser = true;
        home = homeDirectory;
        createHome = true;

        extraGroups = [
          "audio"
          "disk"
          "networkmanager"
          "sway"
          "video"
          "wheel"
        ];
      };
    };
  };
}
