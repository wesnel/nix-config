inputs@
{ pkgs
, lib
, config
, ...
}:

{
  home = {
    activation = lib.mkIf pkgs.stdenv.isDarwin {
      copyApplications = let
        apps = pkgs.buildEnv {
          name = "home-manager-applications";
          paths = config.home.packages;
          pathsToLink = "/Applications";
        };
      in lib.hm.dag.entryAfter [ "writeBoundary" ] ''
        baseDir="$HOME/Applications/Home Manager Apps"
        if [ -d "$baseDir" ]; then
          rm -rf "$baseDir"
        fi
        mkdir -p "$baseDir"
        for appFile in ${apps}/Applications/*; do
          target="$baseDir/$(basename "$appFile")"
          $DRY_RUN_CMD cp ''${VERBOSE_ARG:+-v} -fHRL "$appFile" "$baseDir"
          $DRY_RUN_CMD chmod ''${VERBOSE_ARG:+-v} -R +w "$target"
        done
      '';
    };

    sessionVariables = {
      GPG_TTY = "$(tty)";
      SSH_AUTH_SOCK = "$(gpgconf --list-dirs agent-ssh-socket)";
      KEYID = "0xC9F55C247EBA37F4!";
    };

    shellAliases = let
      e = "$EDITOR";
      ls = "${pkgs.exa}/bin/exa";
      l = "${ls} -laF --color=always --color-scale --icons --git";
      tree = "${ls} -T";
      t = "${l} -T";
    in {
      inherit
        e
        ls
        l
        tree
        t;
    };
  };
}
