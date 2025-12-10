{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.gcloud;
in {
  options.wgn.home.gcloud = {
    enable = mkEnableOption "Enables my gcloud setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      (google-cloud-sdk.withExtraComponents (with google-cloud-sdk.components; [
        cbt
        cloud_sql_proxy
        gke-gcloud-auth-plugin
        kubectl
      ]))
    ];
  };
}
