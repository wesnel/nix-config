inputs@
{ pkgs
, ...
}:

{
  imports = [
    ../mbsync
  ];

  programs.notmuch = {
    enable = true;

    hooks = {
      preNew = "mbsync --all";
    };
  };
}
