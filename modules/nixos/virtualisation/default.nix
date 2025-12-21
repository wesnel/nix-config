{
  config,
  lib,
  username,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.nixos.virtualisation;
in {
  options.wgn.nixos.virtualisation = {
    enable = mkEnableOption "Enables my virtualization setup for NixOS";
  };

  config = mkIf cfg.enable {
    virtualisation = {
      docker = {
        enable = true;

        rootless = {
          enable = true;
          setSocketVariable = true;
        };
      };

      libvirtd = {
        enable = true;

        qemu = {
          package = pkgs.qemu_kvm;
          swtpm.enable = true;
        };
      };

      spiceUSBRedirection.enable = true;
    };

    programs = {
      dconf.enable = true;
      virt-manager.enable = true;
    };

    services = {
      qemuGuest.enable = true;
      spice-vdagentd.enable = true;
    };

    environment.systemPackages = with pkgs; [
      adwaita-icon-theme
      docker-compose
      spice
      spice-gtk
      spice-protocol
      virt-manager
      virt-viewer
      win-spice
      virtio-win
    ];

    users.users.${username}.extraGroups = [
      "libvirtd"
    ];
  };
}
