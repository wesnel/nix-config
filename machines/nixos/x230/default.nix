{ nixos-hardware }:

let
  computerName = "x230";
  username = "wgn";
  homeDirectory = "/home/${username}";
  system = "x86_64-linux";

  homeManagerModules = [
    ../../../modules/home-manager/emacs
    ../../../modules/home-manager/email
    ../../../modules/home-manager/firefox
    ../../../modules/home-manager/fish
    ../../../modules/home-manager/git
    ../../../modules/home-manager/gnupg
    ../../../modules/home-manager/man
    ../../../modules/home-manager/pass
    ../../../modules/home-manager/wayland
    ../../../modules/home-manager/yubikey

    ({ lib
     , config
     , pkgs
     , ... }:

       {
         home.packages = with pkgs; [
           filmulator-gui
           gcc
         ];

         wayland.windowManager.sway = {
           config = {
             keybindings = let
               modifier = config.wayland.windowManager.sway.config.modifier;
               background-image = "${config.home.homeDirectory}/.background-image";
             in lib.mkOptionDefault {
               "XF86Battery" = "exec --no-startup-id ${pkgs.swaylock}/bin/swaylock -elfF -s fill -i ${background-image}";

               # audio
               "XF86AudioRaiseVolume" = "exec --no-startup-id ${pkgs.pulseaudio-ctl}/bin/pulseaudio-ctl up";
               "XF86AudioLowerVolume" = "exec --no-startup-id ${pkgs.pulseaudio-ctl}/bin/pulseaudio-ctl down";
               "XF86AudioMute"        = "exec --no-startup-id ${pkgs.pulseaudio-ctl}/bin/pulseaudio-ctl mute";
               "XF86AudioMicMute"     = "exec --no-startup-id ${pkgs.pulseaudio-ctl}/bin/pulseaudio-ctl mute-input";

               # video
               "XF86MonBrightnessUp"   = "exec --no-startup-id ${pkgs.brightnessctl}/bin/brightnessctl set +5%";
               "XF86MonBrightnessDown" = "exec --no-startup-id ${pkgs.brightnessctl}/bin/brightnessctl set 5%-";

               # media
               "XF86AudioPlay"  = "exec --no-startup-id ${pkgs.mpc_cli}/bin/mpc toggle";
               "XF86AudioPause" = "exec --no-startup-id ${pkgs.mpc_cli}/bin/mpc toggle";
               "XF86AudioNext"  = "exec --no-startup-id ${pkgs.mpc_cli}/bin/mpc next";
               "XF86AudioPrev"  = "exec --no-startup-id ${pkgs.mpc_cli}/bin/mpc prev";

               # include workspace 10
               "${modifier}+0" = "workspace number 10";
               "${modifier}+Shift+0" = "move container to workspace number 10";
             };
           };
         };
       })
  ];

  nixosModules = [
    nixos-hardware.nixosModules.lenovo-thinkpad-x230

    ../../../modules/nixos/fish
    ../../../modules/nixos/fonts
    ../../../modules/nixos/networking
    ../../../modules/nixos/nix
    ../../../modules/nixos/openvpn
    ../../../modules/nixos/users
    ../../../modules/nixos/wayland
    ../../../modules/nixos/yubikey

    ({ pkgs
     , config
     , ... }:

       {
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
           };

           kernelModules = [
             "acpi_call"
             "kvm-intel"
             "thinkpad_acpi"
           ];

           kernelPackages = config.boot.zfs.package.latestCompatibleLinuxPackages;
           kernelParams = [ "nohibernate" ];

           loader.grub = {
             enable = true;
             device = "/dev/sda";
           };

           supportedFilesystems = [ "zfs" ];

           tmp = {
             cleanOnBoot = true;
           };

           zfs = {
             requestEncryptionCredentials = true;
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
             device = "zroot/root/nixos";
             fsType = "zfs";
           };

           "/home" = {
             device = "zroot/home";
             fsType = "zfs";
           };

           "/boot" = {
             device = "/dev/disk/by-uuid/67E6-E2E5";
             fsType = "vfat";
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

           opengl = {
             enable = true;
             driSupport = true;
             driSupport32Bit = true;
             extraPackages = [ pkgs.beignet ];
           };

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

         networking = {
           firewall.allowedTCPPorts = [ 22 ];
           hostId = "6f480610";

           interfaces = {
             eno0.useDHCP = true;
             wlp2s0.useDHCP = true;
           };

           networkmanager = {
             enable = true;
           };

           useDHCP = false;
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

           thinkfan = {
             enable = true;

             levels = [
               [ 0 0  60 ]
               [ 1 53 65 ]
               [ 2 55 66 ]
               [ 3 57 68 ]
               [ 4 61 70 ]
               [ 5 64 71 ]
               [ 7 68 32767 ]
               [ "level full-speed" 68 32767 ]
             ];

             sensors = [
               {
                 query = "/sys/devices/virtual/thermal/thermal_zone0/temp";
                 type = "hwmon";
               }
             ];
           };

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

         sound.enable = true;
       })
  ];
in {
  inherit
    computerName
    username
    homeDirectory
    system
    homeManagerModules
    nixosModules;
}
