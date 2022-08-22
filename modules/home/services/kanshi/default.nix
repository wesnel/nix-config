inputs@
{ config
, pkgs
, lib
, ...
}:

{
  services.kanshi = {
    enable = true;
  };
}
