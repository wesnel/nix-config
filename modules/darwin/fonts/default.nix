{
  config,
  pkgs,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.fonts;
in {
  options.wgn.darwin.fonts = {
    enable = mkEnableOption "Enables my font configuration for Darwin";
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
  };
}
