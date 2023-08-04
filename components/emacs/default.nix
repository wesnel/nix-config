{ lib
, config
, ... }:

{
  config =  {
    programs.emacs = {
      enable = true;
    };

    programs.fish.interactiveShellInit = lib.mkIf config.programs.fish.enable
      "set -gx EDITOR ${config.programs.emacs.package}/bin/emacsclient -t --alternate-editor=''";
  };
}
