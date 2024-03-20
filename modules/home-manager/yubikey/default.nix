{ lib
, config
, ... }:

{
  imports = [
    ../../../components/yubikey
  ];

  config = {
    programs.gpg.scdaemonSettings = lib.mkIf config.programs.gpg.enable {
      disable-ccid = true;
      reader-port = "Yubico YubiKey OTP+FIDO+CCID";
    };
  };
}
