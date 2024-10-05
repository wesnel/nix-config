{
  config,
  lib,
  computerName,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.networking;
in {
  options.wgn.darwin.networking = {
    enable = mkEnableOption "Enables my networking setup for Darwin";
  };

  config = mkIf cfg.enable {
    networking = {
      inherit computerName;

      hostName = computerName;
    };
  };
}
