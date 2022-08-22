inputs@
{ pkgs
, ...
}:

{
  programs.mbsync = {
    enable = true;
  };
}
