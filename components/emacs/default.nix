{ lib
, config
, ... }:

{
  config =  {
    programs.fish.interactiveShellInit = lib.mkIf config.programs.fish.enable
      "set -gx EDITOR emacsclient -t --alternate-editor=''";
  };
}
