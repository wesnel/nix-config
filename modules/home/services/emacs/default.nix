inputs@
  { lib
  , pkgs
  , config
  , ...
  }:

{
  services.emacs = {
    enable = pkgs.stdenv.hostPlatform.isLinux;
    client.enable = true;
    defaultEditor = true;
  };
}
