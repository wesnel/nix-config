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

    profiles = {
      wgn = {
        settings = {
          "toolkit.legacyUserProfileCustomizations.stylesheets" = true;
        };

        userChrome = ''
          .tab-close-button { display:none !important; }
        '';

        extensions = with pkgs.nur.repos.rycee.firefox-addons; [
          bitwarden
          darkreader
          ipfs-companion
          tree-style-tab
          ublock-origin
          umatrix
        ];
      };
    };
  };
}
