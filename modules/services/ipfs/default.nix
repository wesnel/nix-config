inputs@
{ pkgs
, ...
}:

{
  services.ipfs = {
    enable = true;
  };
}
