{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.yubikey;
in {
  options.wgn.nixos.yubikey = {
    enable = mkEnableOption "Enables my Yubikey setup for NixOS";
  };

  config = mkIf cfg.enable {
    programs.ssh.startAgent = false;

    services = {
      pcscd.enable = true;
      udev.packages = with pkgs; [
        yubikey-personalization
        libu2f-host
      ];
    };
  };
}
