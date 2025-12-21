{
  config,
  lib,
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
      enableDefaultPackages = true;
      fontDir.enable = true;
      fontconfig.enable = false;
    };
  };
}
