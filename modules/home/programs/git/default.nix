inputs@
{ pkgs
, ...
}:

{
  programs.git = let
    name = "Wesley Nelson";
    username = "wgn";
    host = "wesnel.dev";
  in {
    enable = true;
    userName = name;
    userEmail = "${username}@${host}";
    package = pkgs.gitAndTools.gitFull;
    delta.enable = true;
    lfs.enable = true;

    # use yubikey to gpg sign git commits
    signing = {
      signByDefault = true;
      key = "0x8AB4F50FF6C15D42";
    };

    extraConfig = {
      init = {
        defaultBranch = "main";
      };
    };
  };
}
