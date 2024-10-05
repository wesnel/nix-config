{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.mullvad;
in {
  options.wgn.nixos.mullvad = {
    enable = mkEnableOption "Enables my Mullvad setup for NixOS";
  };

  config = mkIf cfg.enable {
    services.mullvad-vpn = {
      enable = true;
      package = pkgs.mullvad-vpn;
    };
  };
}
