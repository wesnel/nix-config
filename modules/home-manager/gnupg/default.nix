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

      package = pkgs.gnupg.override {
        pcsclite = pkgs.pcsclite.overrideAttrs (old: {
          # https://discourse.nixos.org/t/gpg-selecting-card-failed-service-is-not-running/44974/18
          postPatch =
            old.postPatch
            + (lib.optionalString (!(lib.strings.hasInfix ''--replace-fail "libpcsclite_real.so.1"'' old.postPatch)) ''
              substituteInPlace src/libredirect.c src/spy/libpcscspy.c \
                --replace-fail "libpcsclite_real.so.1" "$lib/lib/libpcsclite_real.so.1"
            '');
        });
      };

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
      pinentryPackage = pkgs.pinentry-qt;

      extraConfig = ''
        allow-emacs-pinentry
        allow-loopback-pinentry
      '';
    };
  };
}
