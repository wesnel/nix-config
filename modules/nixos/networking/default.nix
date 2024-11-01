{
  config,
  lib,
  computerName,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.networking;
in {
  options.wgn.nixos.networking = {
    enable = mkEnableOption "Enables my networking setup for NixOS";
  };

  config = mkIf cfg.enable {
    networking = {
      firewall.allowedTCPPorts = [
        22
        80
        993
        465
      ];

      hostName = computerName;
      networkmanager.enable = true;
      useDHCP = lib.mkDefault true;
    };

    hardware = {
      bluetooth.enable = true;
    };

    time.timeZone = "America/Los_Angeles";

    i18n = {
      defaultLocale = "en_US.utf8";

      extraLocaleSettings = {
        LC_ADDRESS = "en_US.UTF-8";
        LC_IDENTIFICATION = "en_US.UTF-8";
        LC_MEASUREMENT = "en_US.UTF-8";
        LC_MONETARY = "en_US.UTF-8";
        LC_NAME = "en_US.UTF-8";
        LC_NUMERIC = "en_US.UTF-8";
        LC_PAPER = "en_US.UTF-8";
        LC_TELEPHONE = "en_US.UTF-8";
        LC_TIME = "en_US.UTF-8";
      };
    };
  };
}
