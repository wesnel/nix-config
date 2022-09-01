inputs@
{ pkgs
, ...
}:

{
  services.pcscd = {
    enable = true;
  };
}
