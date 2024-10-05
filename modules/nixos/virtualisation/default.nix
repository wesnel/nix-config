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
      docker.enable = true;

      libvirtd = {
        enable = true;

        qemu = {
          package = pkgs.qemu_kvm;
          swtpm.enable = true;

          ovmf = {
            enable = true;

            packages = with pkgs; [
              OVMFFull.fd
            ];
          };
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
      docker-compose
      gnome.adwaita-icon-theme
      spice
      spice-gtk
      spice-protocol
      virt-manager
      virt-viewer
      win-spice
      win-virtio
    ];

    users.users.${username}.extraGroups = [
      "docker"
      "libvirtd"
    ];
  };
}
