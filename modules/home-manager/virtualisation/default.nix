{
  config,
  lib,
  username,
  ...
}:
with lib; let
  cfg = config.wgn.home.virtualisation;
in {
  options.wgn.home.virtualisation = {
    enable = mkEnableOption "Enables my virtualization setup for home-manager";
  };

  config = mkIf cfg.enable {
    dconf.settings = {
      "org/virt-manager/virt-manager/connections" = {
        autoconnect = ["qemu:///system"];
        uris = ["qemu:///system"];
      };
    };
  };
}
