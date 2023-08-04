{ homeDirectory
, ... }:

{
  config = let
    key = "0xC9F55C247EBA37F4!";
    sshAuthSock = "${homeDirectory}/.gnupg/S.gpg-agent.ssh";
  in {
    environment.variables = {
      SSH_AUTH_SOCK = sshAuthSock;
      KEYID = key;
      GPG_TTY = "$(tty)";
    };
  };
}
