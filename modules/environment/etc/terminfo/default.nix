inputs@
{ pkgs
, config
, ...
}:

{
  environment.etc."terminfo" = {
    source = "${config.system.path}/share/terminfo";
  };
}
