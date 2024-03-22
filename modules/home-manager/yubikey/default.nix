{ lib
, config
, key
, homeDirectory
, ... }:

{
  imports = [
    ../../../components/yubikey
  ];

  config = {
    home.sessionVariables = let
      KEYID = key;
      SSH_AUTH_SOCK = "${homeDirectory}/.gnupg/S.gpg-agent.ssh";
      GPG_TTY = "$(tty)";
    in lib.mkIf config.programs.gpg.enable {
      inherit
        KEYID
        SSH_AUTH_SOCK
        GPG_TTY;
    };

    programs.gpg.scdaemonSettings = lib.mkIf config.programs.gpg.enable {
      disable-ccid = true;
      reader-port = "Yubico YubiKey OTP+FIDO+CCID";
    };
  };
}
