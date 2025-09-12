{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.python;
in {
  options.wgn.home.python = {
    enable = mkEnableOption "Enables my Python setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      (python313.withPackages
        (pp: [
        ]))

      poetry
      ruff
      uv
    ];
  };
}
