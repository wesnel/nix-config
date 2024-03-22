{ lib
, config
, key
, signingKey
, homeDirectory
, ... }:

{
  config = {
    programs.git.signing = lib.mkIf config.programs.git.enable {
      signByDefault = true;
      key = signingKey;
    };

    programs.password-store.settings = lib.mkIf config.programs.password-store.enable {
      PASSWORD_STORE_KEY = key;
      PASSWORD_STORE_SIGNING_KEY = signingKey;
    };

    programs.fish.interactiveShellInit = let
      gpg = config.programs.gpg.package;
    in lib.mkIf config.programs.fish.enable ''
      set -gx GPG_TTY (tty)
      set -gx SSH_AUTH_SOCK ${homeDirectory}/.gnupg/S.gpg-agent.ssh
      set -gx KEYID ${key}

      ${gpg}/bin/gpg-connect-agent updatestartuptty /bye > /dev/null
    '';
  };
}
