{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.flatpak;
in {
  options.wgn.nixos.flatpak = {
    # nixpkgs asserts an XDG desktop portal alongside Flatpak, which each
    # machine supplies for its own desktop.
    enable = mkEnableOption "Enables my Flatpak setup for NixOS";
  };

  config = mkIf cfg.enable {
    services.flatpak.enable = true;
  };
}
