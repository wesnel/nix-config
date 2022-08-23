inputs@
{ pkgs
, ...
}:

{
  virtualisation.docker = {
    enable = true;
  };
}
