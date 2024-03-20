{ pkgs
, ... }:

{
  imports = [
    ../../../components/emacs
  ];

  home = {
    packages = with pkgs; [
      emacs
    ];
    
    programs.wgn.emacs.enable = true;
  };
}
