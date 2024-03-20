{ homeDirectory
, key
, ... }:

{
  config = let
    sshAuthSock = "${homeDirectory}/.gnupg/S.gpg-agent.ssh";
  in {
    environment.variables = {
      SSH_AUTH_SOCK = sshAuthSock;
      KEYID = key;
      GPG_TTY = "$(tty)";
    };
  };
}
