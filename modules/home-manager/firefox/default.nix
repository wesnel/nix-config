{ pkgs
, ... }:

{
  programs = {
    firefox = {
      enable = true;

      profiles.wgn = {
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
