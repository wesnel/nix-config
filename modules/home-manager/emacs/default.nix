{
  pkgs,
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.emacs;
in {
  options.wgn.home.emacs = {
    enable = mkEnableOption "Enables my Emacs setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs = {
      fish.interactiveShellInit =
        lib.mkIf config.programs.fish.enable
        "set -gx EDITOR emacsclient -t --alternate-editor=''";
    };

    home = {
      programs.wgn.emacs.enable = true;

      packages = with pkgs; [
        emacs
      ];
    };
  };
}
