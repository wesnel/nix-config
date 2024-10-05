{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.steam;
in {
  options.wgn.nixos.steam = {
    enable = mkEnableOption "Enables my Steam setup for NixOS";
  };

  config = mkIf cfg.enable {
    hardware.steam-hardware.enable = true;
    programs.steam.enable = true;
  };
}
