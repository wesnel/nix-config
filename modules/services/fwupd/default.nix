inputs@
{ pkgs
, ...
}:

{
  services.fwupd = {
    enable = true;
  };
}
