{
  config,
  lib,
  pkgs,
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
    ];
  };
}
