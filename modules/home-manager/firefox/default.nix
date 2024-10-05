{
  config,
  lib,
  pkgs,
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

        profiles.wgn = {
          userChrome = ''
            .tab-close-button { display:none !important; }
          '';

          extensions = with pkgs.nur.repos.rycee.firefox-addons; [
            bitwarden
            kagi-search
            privacy-badger
            ublock-origin
          ];
        };
      };
    };
  };
}
