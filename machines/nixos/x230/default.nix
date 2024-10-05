{
  nixos-hardware,
  emacs-config,
}: let
  computerName = "x230";
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
        git.enable = true;
        gnupg.enable = true;
        man.enable = true;
        music.enable = true;
        pass.enable = true;
        photos.enable = true;
        sway.enable = true;
        yubikey.enable = true;
      };
    })

    # x230-specific wayland config:
    ./wayland
  ];

  extraNixOSModules = [
    emacs-config.nixosModules.nixos
    nixos-hardware.nixosModules.lenovo-thinkpad-x230

    (_: {
      wgn.nixos = {
        emacs.enable = true;
        fish.enable = true;
        fonts.enable = true;
        networking.enable = true;
        nix.enable = true;
        sddm.enable = true;
        users.enable = true;
        wayland.enable = true;
        yubikey.enable = true;
      };
    })

    ({
      pkgs,
      config,
      ...
    }: {
      boot = {
        initrd = {
          availableKernelModules = [
            "xhci_pci"
            "ehci_pci"
            "ahci"
            "usb_storage"
            "sd_mod"
            "sdhci_pci"
          ];

          luks.devices = {
            # TODO: these UUIDs are automatically generated by
            # NixOS on a fresh install, and it is annoying to have
            # to specify them on git.  these values are found
            # inside /etc/nixos/configuration.nix and
            # /etc/nixos/hardware-configuration.nix.  since
            # flake-based configurations do not necessarily need to
            # overwrite those original files, perhaps we can just
            # import those paths and use only the parts of that
            # configuration which use these ugly UUIDs.
            # specifically: LUKS, swapfile, and file system.
            "luks-4207872f-7abb-4e16-afb4-1c716f20ffa5" = {
              device = "/dev/disk/by-uuid/4207872f-7abb-4e16-afb4-1c716f20ffa5";
              keyFile = "/crypto_keyfile.bin";
            };

            "luks-b1c504f1-f6b3-493d-b74b-9b52bf42a37c" = {
              device = "/dev/disk/by-uuid/b1c504f1-f6b3-493d-b74b-9b52bf42a37c";
              keyFile = "/crypto_keyfile.bin";
            };
          };

          secrets = {
            "/crypto_keyfile.bin" = null;
          };
        };

        kernelModules = [
          "acpi_call"
          "kvm-intel"
          "thinkpad_acpi"
        ];

        kernelPackages = pkgs.linuxPackages_latest;

        kernelParams = [
          "iomem=relaxed"
          "quiet"
          "splash"
        ];

        loader.grub = {
          enable = true;
          device = "/dev/sda";
          enableCryptodisk = true;
        };

        tmp = {
          cleanOnBoot = true;
        };
      };

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
          device = "/dev/disk/by-uuid/c178d62a-806a-41f5-ab27-e4a96e795d17";
          fsType = "ext4";
        };
      };

      hardware = {
        bluetooth = {
          enable = true;

          settings = {
            General = {
              Enable = "Source,Sink,Media,Socket";
            };
          };
        };

        cpu.intel.updateMicrocode = true;
        enableRedistributableFirmware = true;
        graphics.enable = true;

        pulseaudio = {
          enable = true;
          support32Bit = true;
          package = pkgs.pulseaudioFull;

          extraConfig = ''
            # automatically switch to newly-connected devices
            load-module module-switch-on-connect
          '';
        };
      };

      powerManagement.powertop.enable = true;

      services = {
        acpid.enable = true;
        blueman.enable = true;

        logind = {
          lidSwitch = "suspend";
          lidSwitchDocked = "suspend";
        };

        openssh = {
          enable = true;

          settings = {
            PasswordAuthentication = false;
          };
        };

        printing.enable = true;

        tlp = {
          enable = true;

          settings = {
            SATA_LINKPWR_ON_BAT = "max_performance";
            STOP_CHARGE_THRESH_BAT0 = 80;
            CPU_SCALING_GOVERNOR_ON_BAT = "powersave";
            ENERGY_PERF_POLICY_ON_BAT = "powersave";
          };
        };

        udev.extraRules = ''
          SUBSYSTEM=="usb", ATTRS{idVendor}=="28de", MODE="0666"
          KERNEL=="uinput", MODE="0660", GROUP="wheel", OPTIONS+="static_node=uinput"
          KERNEL=="hidraw*", ATTRS{idVendor}=="057e", ATTRS{idProduct}=="2009", MODE="0666"
          KERNEL=="hidraw*", KERNELS=="*057E:2009*", MODE="0666"
        '';

        upower.enable = true;
      };

      swapDevices = [
        {
          device = "/dev/disk/by-uuid/6b4f50ab-03bd-4c7a-937c-bc6287cf5647";
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
