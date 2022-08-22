inputs@
{ pkgs
, ...
}:

{
  services.nix-daemon = {
    enable = true;
  };
}
