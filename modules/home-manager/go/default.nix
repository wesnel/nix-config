{
  config,
  lib,
  pkgs,
  homeDirectory,
  ...
}:
with lib; let
  cfg = config.wgn.home.go;
in {
  options.wgn.home.go = {
    enable = mkEnableOption "Enables my Go setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      go
      gopls
      delve
    ];

    programs = {
      # TODO: Remove Shipt-specific configuration from here.
      fish.interactiveShellInit = lib.mkIf config.programs.fish.enable ''
        set -gx GOPRIVATE 'github.com/shipt/*'

        fish_add_path ${homeDirectory}/go/bin
      '';
    };
  };
}
