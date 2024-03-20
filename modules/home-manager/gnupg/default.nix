{ pkgs
, key
, ... }:

{
  programs.gpg = {
    enable = true;

    publicKeys = [
      {
        source = ./F84480B20CA9D6CCC7F52479A776D2AD099E8BC0.asc;
        trust = 5;
      }
    ];

    settings = {
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

    sshKeys = [
      "E1A99D519849CB5FE1C1AE4D88B2AA7DD529E17D"
    ];
  };
}
