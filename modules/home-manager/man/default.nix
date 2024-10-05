{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.man;
in {
  options.wgn.home.man = {
    enable = mkEnableOption "Enables my manpages setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs.man = {
      enable = true;
    };
  };
}
