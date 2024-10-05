{
  config,
  lib,
  username,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.virtualisation;
in {
  options.wgn.nixos.virtualisation = {
    enable = mkEnableOption "Enables my virtualization setup for NixOS";
  };

  config = mkIf cfg.enable {
    virtualisation.docker.enable = true;
    environment.systemPackages = with pkgs; [docker-compose];
    users.users.${username}.extraGroups = ["docker"];
  };
}
