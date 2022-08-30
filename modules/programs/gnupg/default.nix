inputs@
{ pkgs
, ...
}:

{
  programs.gnupg = {
    agent = {
      enable = true;
      enableSSHSupport = true;
    };
  };
}
