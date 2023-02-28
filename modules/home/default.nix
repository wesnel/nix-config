inputs@
{ pkgs
, lib
, config
, ...
}:

{
  disabledModules = ["targets/darwin/linkapps.nix"]; # to use my aliasing instead

  home = {
    sessionVariables = {
      GPG_TTY = "$(tty)";
      SSH_AUTH_SOCK = "$(gpgconf --list-dirs agent-ssh-socket)";
      KEYID = "0xC9F55C247EBA37F4!";
    };

    activation.aliasApplications = lib.mkIf pkgs.stdenv.hostPlatform.isDarwin
      (lib.hm.dag.entryAfter ["writeBoundary"] ''
        app_folder="Home Manager Apps"
        app_path="$(echo ~/Applications)/$app_folder"
        tmp_path="$(mktemp -dt "$app_folder.XXXXXXXXXX")" || exit 1
        # NB: aliasing ".../home-path/Applications" to
        #    "~/Applications/Home Manager Apps" doesn't work (presumably
        #     because the individual apps are symlinked in that directory, not
        #     aliased). So this makes "Home Manager Apps" a normal directory
        #     and then aliases each application into there directly from its
        #     location in the nix store.
        for app in \
          $(find "$newGenPath/home-path/Applications" -type l -exec \
            readlink -f {} \;)
        do
          $DRY_RUN_CMD /usr/bin/osascript \
            -e "tell app \"Finder\"" \
            -e "make new alias file at POSIX file \"$tmp_path\" \
                                    to POSIX file \"$app\"" \
            -e "set name of result to \"$(basename $app)\"" \
            -e "end tell"
        done
        # TODO: Wish this was atomic, but it’s only tossing symlinks
        $DRY_RUN_CMD [ -e "$app_path" ] && rm -r "$app_path"
        $DRY_RUN_CMD mv "$tmp_path" "$app_path"
      '');

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
