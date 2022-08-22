inputs@
{ pkgs
, ...
}:

{
  programs.fish = {
    enable = true;

    interactiveShellInit = ''
      set -x GPG_TTY (tty)
      set -x SSH_AUTH_SOCK (gpgconf --list-dirs agent-ssh-socket)
      set -x KEYID "0xC9F55C247EBA37F4!"

      gpgconf --launch gpg-agent
    '';
  };
}
