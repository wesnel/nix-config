inputs@
{ pkgs
, ...
}:

{
  nixpkgs.config.allowUnfree = true;
}
