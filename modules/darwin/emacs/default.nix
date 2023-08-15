{ pkgs
, ... }:

{
  imports = [
    ../../../components/emacs
  ];

  config = {
    environment.systemPackages = with pkgs; [
      emacs
    ];

    services.emacs = {
      enable = true;
    };
  };
}
