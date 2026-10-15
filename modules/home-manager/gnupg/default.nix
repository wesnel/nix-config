{
  config,
  pkgs,
  lib,
  key,
  ...
}:
with lib; let
  cfg = config.wgn.home.gnupg;
in {
  options.wgn.home.gnupg = {
    enable = mkEnableOption "Enables my GnuPG setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs.gpg = {
      enable = true;

      publicKeys = [
        {
          source = ../../../keys/users/F84480B20CA9D6CCC7F52479A776D2AD099E8BC0.asc;
          trust = 5;
        }
      ];

      settings = {
        armor = true;
        charset = "utf-8";
        default-key = key;
        keyid-format = "0xlong";
        keyserver = "hkps://keys.openpgp.org";
        no-comments = true;
        no-emit-version = true;
        no-greeting = true;
        no-symkey-cache = true;
        require-cross-certification = true;
        use-agent = true;
        with-fingerprint = true;
      };
    };

    services.gpg-agent = {
      enable = pkgs.stdenv.hostPlatform.isLinux;
      enableSshSupport = true;
      pinentry.package = pkgs.pinentry-qt;

      extraConfig = ''
        allow-emacs-pinentry
        allow-loopback-pinentry
      '';
    };
  };
}
