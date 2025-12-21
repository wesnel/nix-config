{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.fonts;
in {
  options.wgn.home.fonts = {
    enable = mkEnableOption "Enables my font setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs;
      [
        font-awesome
        fontconfig
        unifont
      ]
      ++ builtins.filter attrsets.isDerivation (builtins.attrValues pkgs.nerd-fonts);

    fonts.fontconfig = {
      enable = true;

      defaultFonts = {
        emoji = [
          "Noto Color Emoji"
          "FontAwesome"
        ];

        monospace = [
          "ComicShannsMono Nerd Font Mono"
          "FontAwesome"
        ];

        sansSerif = [
          "Ubuntu Nerd Font Propo"
          "FontAwesome"
        ];

        serif = [
          "LiberationSerif Nerd Font Propo"
          "FontAwesome"
        ];
      };
    };
  };
}
