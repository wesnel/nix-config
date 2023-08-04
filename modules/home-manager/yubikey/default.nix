{ homeDirectory
, lib
, config
, ... }:

{
  imports = [
    ../../../components/yubikey
  ];

  config = let
    KEYID = "0xC9F55C247EBA37F4!";
    SSH_AUTH_SOCK = "${homeDirectory}/.gnupg/S.gpg-agent.ssh";
  in {
    home.sessionVariables = lib.mkIf config.programs.gpg.enable {
      inherit
        KEYID
        SSH_AUTH_SOCK;

      GPG_TTY = "$(tty)";
    };

    programs.gpg.scdaemonSettings = lib.mkIf config.programs.gpg.enable {
      disable-ccid = true;
      reader-port = "Yubico YubiKey OTP+FIDO+CCID";
    };
  };
}
