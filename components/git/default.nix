{ pkgs
, ... }:

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

    extraConfig = {
      init = {
        defaultBranch = "main";
      };
    };
  };
}
