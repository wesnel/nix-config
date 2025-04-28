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
      packages = with pkgs;
        [
          inter
          unifont
        ]
        ++ builtins.filter attrsets.isDerivation (builtins.attrValues pkgs.nerd-fonts);
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
