{
  nixos-hardware,
  emacs-config,
}: let
  computerName = "framework";
  username = "wgn";
  homeDirectory = "/home/${username}";
  system = "x86_64-linux";

  extraHomeManagerModules = [
    emacs-config.nixosModules.home

    (_: {
      wgn.home = {
        emacs.enable = true;
        email.enable = true;
        firefox.enable = true;
        fish.enable = true;
        foot.enable = true;
        gamedev.enable = true;
        git.enable = true;
        gnupg.enable = true;
        man.enable = true;
        music.enable = true;
        pass.enable = true;
        photos.enable = true;
        video.enable = true;
        virtualisation.enable = true;
        yubikey.enable = true;
      };
    })
  ];

  extraNixOSModules = [
    emacs-config.nixosModules.nixos
    nixos-hardware.nixosModules.framework-12th-gen-intel

    (_: {
      wgn.nixos = {
        emacs.enable = true;
        fish.enable = true;
        fonts.enable = true;
        kde.enable = true;
        interception-tools.enable = true;
        mullvad.enable = true;
        networking.enable = true;
        nix.enable = true;
        sddm.enable = true;
        steam.enable = true;
        users.enable = true;
        virtualisation.enable = true;
        yubikey.enable = true;
      };
    })

    ({pkgs, ...}: {
      hardware = {
        cpu.intel.updateMicrocode = true;
        enableRedistributableFirmware = true;
        graphics.enable = true;
      };

      boot = {
        loader = {
          systemd-boot.enable = true;

          efi = {
            canTouchEfiVariables = true;
            efiSysMountPoint = "/boot/efi";
          };
        };

        initrd = {
          availableKernelModules = [
            "xhci_pci"
            "thunderbolt"
            "nvme"
            "uas"
            "usb_storage"
            "sd_mod"
          ];

          kernelModules = [];

          secrets = {
            "/crypto_keyfile.bin" = null;
          };

          luks.devices = {
            "luks-41c779d6-a392-4e87-9acb-9e2e97073539" = {
              device = "/dev/disk/by-uuid/41c779d6-a392-4e87-9acb-9e2e97073539";
              keyFile = "/crypto_keyfile.bin";
            };

            "luks-fac85ca9-b8da-41c9-8a68-e908a8ea5928" = {
              device = "/dev/disk/by-uuid/fac85ca9-b8da-41c9-8a68-e908a8ea5928";
            };
          };
        };

        kernelModules = ["kvm-intel"];
        kernelPackages = pkgs.linuxPackages_latest;
        extraModulePackages = [];
      };

      fileSystems = {
        "/" = {
          device = "/dev/disk/by-uuid/28fafdc3-1134-4616-9060-f75f6572c415";
          fsType = "ext4";
        };

        "/boot/efi" = {
          device = "/dev/disk/by-uuid/C3F0-58AF";
          fsType = "vfat";
        };
      };

      services = {
        pipewire = {
          enable = true;

          alsa = {
            enable = true;
            support32Bit = true;
          };

          pulse.enable = true;
        };
      };

      swapDevices = [
        {
          device = "/dev/disk/by-uuid/9fb715ce-898c-4a5a-80fd-cd19b7052784";
        }
      ];
    })
  ];
in {
  inherit
    computerName
    username
    homeDirectory
    system
    extraHomeManagerModules
    extraNixOSModules
    ;
}
