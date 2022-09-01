inputs@
{ pkgs
, config
, ...
}:

{
  programs.fish = {
    enable = true;

    interactiveShellInit = let
      gpgPkg = config.programs.gpg.package;
    in ''
      set -gx SSH_AUTH_SOCK (gpgconf --list-dirs agent-ssh-socket)
      set -gx KEYID "0xC9F55C247EBA37F4!"

      ${gpgPkg}/bin/gpg-connect-agent updatestartuptty /bye > /dev/null
    '';
  };
}
