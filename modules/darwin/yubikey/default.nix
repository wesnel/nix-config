{
  config,
  lib,
  homeDirectory,
  key,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.yubikey;
in {
  options.wgn.darwin.yubikey = {
    enable = mkEnableOption "Enables my Yubikey setup for Darwin";
  };

  config = mkIf cfg.enable {
    environment.variables = let
      sshAuthSock = "${homeDirectory}/.gnupg/S.gpg-agent.ssh";
    in {
      SSH_AUTH_SOCK = sshAuthSock;
      KEYID = key;
      GPG_TTY = "$(tty)";
    };
  };
}
