{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.ollama;
in {
  options.wgn.home.ollama = {
    enable = mkEnableOption "Enables my AI setup for home-manager";
  };

  # TODO: Configure AI packages in Emacs if that's enabled?
  config = mkIf cfg.enable {
    services.ollama = {
      enable = true;
    };
  };
}
