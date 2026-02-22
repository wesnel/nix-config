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

    # TODO: Remove Shipt-specific configuration from here.
    programs = {
      git.settings = {
        url = {
          "git@github.com:shipt" = {
            insteadOf = "https://github.com/shipt";
          };
        };
      };

      fish.interactiveShellInit = lib.mkIf config.programs.fish.enable ''
        set -gx GOPRIVATE 'github.com/shipt/*'

        fish_add_path ${homeDirectory}/go/bin
      '';
    };
  };
}
