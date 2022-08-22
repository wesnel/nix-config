inputs@
{ pkgs
, ...
}:

{
  home-manager = {
    useGlobalPkgs = true;
    useUserPackages = true;
  };
}
