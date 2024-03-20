{ nixos-hardware
, emacs-config }:

let
  computerName = "x230";
  username = "wgn";
  homeDirectory = "/home/${username}";
  system = "x86_64-linux";

  homeManagerModules = [
    emacs-config.nixosModules.home

    ../../../modules/home-manager/emacs
    ../../../modules/home-manager/email
    # ../../../modules/home-manager/filmulator
    ../../../modules/home-manager/firefox
    ../../../modules/home-manager/fish
    ../../../modules/home-manager/git
    ../../../modules/home-manager/gnupg
    ../../../modules/home-manager/man
    ../../../modules/home-manager/pass
    ../../../modules/home-manager/wayland
    ../../../modules/home-manager/yubikey

    # x230-specific wayland config:
    ./wayland
  ];

  nixosModules = [
    emacs-config.nixosModules.nixos
    nixos-hardware.nixosModules.lenovo-thinkpad-x230

    ../../../modules/nixos/emacs
    ../../../modules/nixos/fish
    ../../../modules/nixos/fonts
    ../../../modules/nixos/networking
    ../../../modules/nixos/nix
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

             luks.devices = {
               "luks-218b6575-8a29-4780-b81f-7902dcb96081" = {
                 device = "/dev/disk/by-uuid/218b6575-8a29-4780-b81f-7902dcb96081";
                 keyFile = "/crypto_keyfile.bin";
               };

               "luks-06fd1c4e-6e98-4096-b71b-222651992289" = {
                 device = "/dev/disk/by-uuid/06fd1c4e-6e98-4096-b71b-222651992289";
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
             device = "/dev/disk/by-uuid/27f44223-029e-4a1f-90c0-c7bc1fe38eaa";
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

           opengl = {
             enable = true;
             driSupport = true;
             driSupport32Bit = true;
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

         sound.enable = true;

         swapDevices = [
           {
             device = "/dev/disk/by-uuid/21677623-f6fc-4c77-a51b-aaf663b0776d";
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
    homeManagerModules
    nixosModules;
}
