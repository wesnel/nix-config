{
  config,
  lib,
  pkgs,
  username,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.fish;
in {
  options.wgn.nixos.fish = {
    enable = mkEnableOption "Enables my Fish setup for NixOS";
  };

  config = mkIf cfg.enable {
    programs.fish = {
      enable = true;
    };

    users = {
      defaultUserShell = "${pkgs.fish}/bin/fish";
      users.${username}.shell = "${pkgs.fish}/bin/fish";
    };
  };
}
