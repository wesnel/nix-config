{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.fonts;
in {
  options.wgn.nixos.fonts = {
    enable = mkEnableOption "Enables my fonts setup for NixOS";
  };

  config = mkIf cfg.enable {
    fonts = {
      packages = with pkgs; [
        inter
        inter-ui
        nerdfonts
        unifont
      ];
    };

    fonts = {
      enableDefaultPackages = true;

      fontconfig = {
        defaultFonts = {
          monospace = [
            "Iosevka Term"
          ];
        };
      };
    };
  };
}
