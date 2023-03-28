inputs@
  { lib
  , pkgs
  , config
  , ...
  }:

{
  services.emacs = {
    enable = true;
    client.enable = true;
    defaultEditor = true;
  };
}
