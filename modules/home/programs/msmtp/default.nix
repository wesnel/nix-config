inputs@
{ pkgs
, ...
}:

{
  programs.msmtp = {
    enable = true;
  };
}
