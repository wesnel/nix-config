{ pkgs
, ... }:

{
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
}
