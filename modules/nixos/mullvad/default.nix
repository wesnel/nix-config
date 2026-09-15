{
  config,
  lib,
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
      enableEarlyBootBlocking = true;

      # The daemon and the desktop app are separate packages; `package` holds
      # the daemon, so the app has to be asked for on its own.
      gui = {
        enable = true;
      };
    };
  };
}
