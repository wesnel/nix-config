{ lib
, config
, ... }:

{
  config = let
    gpgPkg = config.programs.gpg.package;
    key = "0xC9F55C247EBA37F4!";
    signingKey = "0x8AB4F50FF6C15D42!";
    sshAuthSock = "(${gpgPkg}/bin/gpgconf --list-dirs agent-ssh-socket)";
  in {
    programs.git.signing = lib.mkIf config.programs.git.enable {
      signByDefault = true;
      key = signingKey;
    };

    programs.password-store.settings = lib.mkIf config.programs.password-store.enable {
      PASSWORD_STORE_KEY = key;
      PASSWORD_STORE_SIGNING_KEY = signingKey;
    };

    programs.fish.interactiveShellInit = lib.mkIf config.programs.fish.enable ''
      set -gx SSH_AUTH_SOCK ${sshAuthSock}
      set -gx KEYID "${key}"

      ${gpgPkg}/bin/gpg-connect-agent updatestartuptty /bye > /dev/null
    '';
  };
}
