{ pkgs
, ... }:

{
  imports = [
    ../../../components/emacs
  ];

  programs.wgn.emacs = {
    enable = true;
    package = pkgs.emacs;
  };

  services.emacs = {
    enable = true;
  };
}
