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
    enable = mkEnableOption "Enables my Firefox setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs = {
      firefox = {
        enable = true;
        package = mkIf pkgs.stdenv.isDarwin (makeOverridable ({...}: pkgs.zen-browser-bin) {});
        configPath = mkIf pkgs.stdenv.isDarwin "Library/Application Support/zen";

        profiles."${username}" = {
          extensions.packages = with pkgs.nur.repos.rycee.firefox-addons; [
            bitwarden
            kagi-search
            privacy-badger
            pwas-for-firefox
            ublock-origin
          ];
        };
      };
    };
  };
}
