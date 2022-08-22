inputs@
{ pkgs
, ...
}:

{
  services.printing = {
    enable = true;
  };
}
