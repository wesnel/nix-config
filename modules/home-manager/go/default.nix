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
    shipt.enable = mkEnableOption "Enables my Shipt-specific Go setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      delve
      go
      gofumpt
      golangci-lint
      gopls
      govulncheck
    ];

    # TODO: Remove Shipt-specific configuration from here.
    programs.git.settings = mkIf cfg.shipt.enable {
      url = {
        "git@github.com:shipt" = {
          insteadOf = "https://github.com/shipt";
        };
      };
    };

    programs.fish.interactiveShellInit = let
      shipt =
        if cfg.shipt.enable
        then ''
          set -gx GOPRIVATE 'github.com/shipt/*'
        ''
        else "";
    in ''
      fish_add_path ${homeDirectory}/go/bin
      ${shipt}
    '';
  };
}
