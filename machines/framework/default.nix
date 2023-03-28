inputs@
{ config
, pkgs
, lib
, ...
}:

{
  imports = [
    ../../modules/fonts
    ../../modules/nix
    ../../modules/nixpkgs
    ../../modules/programs/fish
    ../../modules/programs/steam
    ../../modules/services/fwupd
    ../../modules/services/interception-tools
    ../../modules/services/kde
    ../../modules/services/mullvad-vpn
    ../../modules/services/pcscd
    ../../modules/services/printing
    ../../modules/services/udev
    ../../modules/users/wgn
    ../../modules/virtualisation
  ];

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

      kernelModules = [ ];

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

    kernelModules = [ "kvm-intel" ];
    kernelPackages = pkgs.linuxPackages_latest;
    extraModulePackages = [ ];
  };

  environment = {
    enableAllTerminfo = true;
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

  swapDevices = [
    {
      device = "/dev/disk/by-uuid/9fb715ce-898c-4a5a-80fd-cd19b7052784";
    }
  ];

  networking = {
    hostName = "framework";
    networkmanager.enable = true;
    useDHCP = false;

    interfaces = {
      wlp166s0 = {
        useDHCP = true;
      };
    };
  };

  time.timeZone = "America/Los_Angeles";
  i18n.defaultLocale = "en_US.utf8";

  hardware = {
    bluetooth.enable = true;
    cpu.intel.updateMicrocode = true;
    enableRedistributableFirmware = true;
    opengl.enable = true;
  };

  system = {
    stateVersion = "22.05";
  };
}
