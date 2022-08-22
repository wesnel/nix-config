inputs@
{ config
, lib
, pkgs
, ...
}:

{
  programs.afew = {
    enable = true;
  };
}
