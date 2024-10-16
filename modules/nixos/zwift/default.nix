{
  lib,
  config,
  ...
}: let
  inherit
    (lib)
    getExe
    mkEnableOption
    mkIf
    ;

  cfg = config.wgn.nixos.zwift;
in {
  options.wgn.nixos.zwift = {
    offline = mkEnableOption "Enable Zwift offline";
  };

  config = mkIf cfg.offline {
    networking.extraHosts = ''
      127.0.0.1 us-or-rly101.zwift.com secure.zwift.com cdn.zwift.com launcher.zwift.com
    '';

    nixpkgs.overlays = [
      (final: prev:
        mkIf cfg.offline {
          zwift-image = prev.zwift-image.overrideAttrs {
            config = let
              entrypoint = final.writeShellApplication {
                name = "entrypoint-script";

                text = ''
                  ${final.coreutils}/bin/cat \
                  ${final.zwift-offline-cert}/cert-zwift-com.pem \
                  >> "$HOME/.wine/drive_c/Program Files (x86)/Zwift/data/cacert.pem" \
                  && ${final.coreutils}/bin/echo \
                  '127.0.0.1 us-or-rly101.zwift.com secure.zwift.com cdn.zwift.com launcher.zwift.com' \
                  >> "$HOME/.wine/drive_c/Windows/System32/Drivers/etc/hosts" \
                  && entrypoint
                '';
              };
            in {
              Entrypoint = [
                (getExe entrypoint)
              ];
              Cmd = ["$@"];
            };
          };
        })
    ];
  };
}
