inputs@
{ config
, pkgs
, lib
, ...
}:

{
  services.gammastep = {
    enable = true;
    latitude = "33.522861";
    longitude = "-86.807701";
  };
}
