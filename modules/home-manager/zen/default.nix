{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.zen;
in {
  options.wgn.home.zen = {
    # Installed from Flathub by nix-flatpak, whose home-manager module is on
    # only where the system enables Flatpak (wgn.nixos.flatpak). Darwin
    # installs Zen with wgn.darwin.zen instead.
    enable = mkEnableOption "Enables my Zen browser setup for home-manager";
  };

  config = mkIf cfg.enable {
    services.flatpak.packages = [
      "app.zen_browser.zen"
    ];
  };
}
