{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.zellij;
in {
  options.wgn.home.zellij = {
    enable = mkEnableOption "Enables my Zellij setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs.zellij = {
      enable = true;

      # The shell integrations start Zellij automatically for every
      # interactive shell, which nests sessions inside an existing one.
      enableBashIntegration = false;
      enableFishIntegration = false;
    };
  };
}
