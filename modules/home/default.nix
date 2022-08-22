inputs@
{ pkgs
, ...
}:

{
  home = {
    sessionVariables = {
      GPG_TTY = "$(tty)";
      SSH_AUTH_SOCK = "$(gpgconf --list-dirs agent-ssh-socket)";
      KEYID = "0xC9F55C247EBA37F4!";
    };

    shellAliases = let
      ls = "${pkgs.exa}/bin/exa";
      l = "${ls} -laF --color=always --color-scale --icons --git";
      tree = "${ls} -T";
      t = "${l} -T";
    in {
      inherit
        ls
        l
        tree
        t;
    };
  };
}
