inputs@
{ config
, lib
, pkgs
, ...
}:

{
  programs.gpg = {
    enable = true;

    publicKeys = [
      {
        source = ../../users/wgn/F84480B20CA9D6CCC7F52479A776D2AD099E8BC0.asc;
        trust = 5;
      }
    ];

    settings = {
      charset = "utf-8";
      default-key = "0xC9F55C247EBA37F4!";
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

    scdaemonSettings = {
      disable-ccid = true;
      reader-port = "Yubico YubiKey OTP+FIDO+CCID";
    };
  };
}
