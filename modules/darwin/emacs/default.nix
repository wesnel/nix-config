{ pkgs
, lib
, config
, ... }:

{
  config = {
    environment.systemPackages = with pkgs; [
      emacs
    ];

    programs.fish.interactiveShellInit = lib.mkIf config.programs.fish.enable
      "set -gx EDITOR ${config.services.emacs.package}/bin/emacsclient -t --alternate-editor=''";

    services.emacs = {
      enable = true;
    };
  };
}
