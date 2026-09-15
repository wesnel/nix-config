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
