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
      firefox = let
        pkg =
          if pkgs.stdenv.isDarwin
          then makeOverridable ({...}: pkgs.firefox-bin) {}
          else pkgs.firefox;
      in {
        enable = true;
        package = pkg;

        profiles."${username}" = {
          userChrome = ''
            .tab-close-button { display:none !important; }
          '';

          extensions.packages = with pkgs.nur.repos.rycee.firefox-addons; [
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
