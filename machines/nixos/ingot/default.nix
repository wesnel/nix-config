{
  nixos-hardware,
  emacs-config,
}: let
  computerName = "ingot";
  username = "wgn";
  homeDirectory = "/home/${username}";
  system = "x86_64-linux";

  extraHomeManagerModules = [
    emacs-config.homeManagerModules.default

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

    (_: {
      wgn.nixos = {
        emacs.enable = true;
        fish.enable = true;
        fonts.enable = true;
        kde.enable = true;
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

    ({
      pkgs,
      config,
      ...
    }: {
      boot = {
        loader = {
          systemd-boot.enable = true;
          efi.canTouchEfiVariables = true;
        };

        initrd = {
          availableKernelModules = [
            "nvme"
            "xhci_pci"
            "ahci"
            "usb_storage"
            "usbhid"
            "sd_mod"
          ];

          luks.devices = {
            "luks-46ba018a-275c-41c1-baae-4401da3f399f" = {
              device = "/dev/disk/by-uuid/46ba018a-275c-41c1-baae-4401da3f399f";
            };

            "luks-9f8c9e84-d147-4537-84cd-747c1d88c8e9" = {
              device = "/dev/disk/by-uuid/9f8c9e84-d147-4537-84cd-747c1d88c8e9";
            };
          };
        };

        kernelModules = [
          "kvm-amd"
        ];

        tmp = {
          cleanOnBoot = true;
        };
      };

      swapDevices = [
        {
          device = "/dev/disk/by-uuid/0c55a563-f5de-4b18-a933-92b303690f15";
        }
      ];

      console.keyMap = "us";

      documentation = {
        enable = true;
        dev.enable = true;
        doc.enable = true;
        man.enable = true;
        info.enable = true;

        nixos = {
          enable = true;
          includeAllModules = true;
        };
      };

      fileSystems = {
        "/" = {
          device = "/dev/disk/by-uuid/28cb668c-ff29-4bef-9381-2a4c39897458";
          fsType = "ext4";
        };

        "/boot" = {
          device = "/dev/disk/by-uuid/8B69-07CC";
          fsType = "vfat";
        };
      };

      hardware = {
        bluetooth.enable = true;
        cpu.amd.updateMicrocode = true;
        enableRedistributableFirmware = true;
        graphics.enable = true;
        pulseaudio.enable = false;
      };

      networking = {
        interfaces = {
          enp37s0.useDHCP = true;
          wlp38s0.useDHCP = true;
          wlp3s0f0u5.useDHCP = true;
        };

        networkmanager = {
          enable = true;
        };
      };

      security = {
        rtkit.enable = true;
      };

      services = {
        openssh = {
          enable = true;

          settings = {
            PasswordAuthentication = false;
          };
        };

        printing.enable = true;

        pipewire = {
          enable = true;

          alsa = {
            enable = true;
            support32Bit = true;
          };

          pulse = {
            enable = true;
          };
        };
      };
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
