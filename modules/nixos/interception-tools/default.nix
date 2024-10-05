{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.interception-tools;
in {
  options.wgn.nixos.interception-tools = {
    enable = mkEnableOption "Enables my interception-tools setup for NixOS";
  };

  config = mkIf cfg.enable {
    services.interception-tools = {
      enable = true;
      plugins = [pkgs.interception-tools-plugins.caps2esc];

      udevmonConfig = ''
        - JOB: "${pkgs.interception-tools}/bin/intercept -g $DEVNODE | ${pkgs.interception-tools}/bin/uinput -d $DEVNODE"
          DEVICE:
            LINK: /dev/input/by-id/.*HHKB.*event-kbd
        - JOB: "${pkgs.interception-tools}/bin/intercept -g $DEVNODE | ${pkgs.interception-tools-plugins.caps2esc}/bin/caps2esc | ${pkgs.interception-tools}/bin/uinput -d $DEVNODE"
          DEVICE:
            EVENTS:
              EV_KEY: [KEY_CAPSLOCK, KEY_ESC]
      '';
    };
  };
}
