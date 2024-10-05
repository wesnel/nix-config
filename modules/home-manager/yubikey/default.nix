{
  lib,
  config,
  key,
  signingKey,
  ...
}:
with lib; let
  cfg = config.wgn.home.yubikey;
in {
  options.wgn.home.yubikey = {
    enable = mkEnableOption "Enables my Yubikey setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs = {
      git.signing = lib.mkIf config.programs.git.enable {
        signByDefault = true;
        key = signingKey;
      };

      password-store.settings = lib.mkIf config.programs.password-store.enable {
        PASSWORD_STORE_KEY = key;
        PASSWORD_STORE_SIGNING_KEY = signingKey;
      };

      gpg.scdaemonSettings = lib.mkIf config.programs.gpg.enable {
        disable-ccid = true;
        reader-port = "Yubico YubiKey OTP+FIDO+CCID";
      };

      fish.interactiveShellInit = lib.mkIf config.programs.fish.enable ''
        set -gx GPG_TTY (tty)
        set -gx SSH_AUTH_SOCK (gpgconf --list-dirs agent-ssh-socket)
        set -gx KEYID ${key}

        gpg-connect-agent updatestartuptty /bye > /dev/null
      '';
    };
  };
}
