inputs@
{ pkgs
, ...
}:

{
  programs.firefox = {
    enable = true;

    package = pkgs.firefox.override {
      cfg = {
        enableTridactylNative = true;
      };
    };

    extensions = with pkgs.nur.repos.rycee.firefox-addons; [
      bitwarden
      darkreader
      ipfs-companion
      tridactyl
      ublock-origin
      umatrix
    ];

    profiles = {
      wgn = {
        settings = {
          "toolkit.legacyUserProfileCustomizations.stylesheets" = true;
        };

        userChrome = ''
          .tab-close-button { display:none !important; }
        '';
      };
    };
  };
}
