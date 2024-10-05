{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.sddm;
in {
  options.wgn.nixos.sddm = {
    enable = mkEnableOption "Enables my SDDM setup for NixOS";
  };

  config = mkIf cfg.enable {
    services.displayManager.sddm = {
      enable = true;
      wayland.enable = true;
    };
  };
}
