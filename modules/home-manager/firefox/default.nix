{
  config,
  lib,
  pkgs,
  username,
  ...
}:
with lib; let
  cfg = config.wgn.home.firefox;
in {
  options.wgn.home.firefox = {
    enable = mkEnableOption "Enables my Firefox/Zen setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs = {
      firefox = {
        enable = true;
        package = mkIf pkgs.stdenv.hostPlatform.isDarwin (makeOverridable ({...}: pkgs.zen-browser-bin) {});
        configPath =
          if pkgs.stdenv.hostPlatform.isDarwin
          then "Library/Application Support/zen"
          else "${config.xdg.configHome}/mozilla/firefox";

        profiles."${username}" = {
          extensions.packages = with pkgs.nur.repos.rycee.firefox-addons; [
            kagi-search
            kagi-translate
            onepassword-password-manager
            privacy-badger
            ublock-origin
          ];
        };
      };
    };
  };
}
