{ nixos-hardware
, emacs-config }:

let
  computerName = "framework";
  username = "wgn";
  homeDirectory = "/home/${username}";
  system = "x86_64-linux";

  homeManagerModules = [
    emacs-config.nixosModules.home

    ../../../modules/home-manager/emacs
    ../../../modules/home-manager/email
    ../../../modules/home-manager/firefox
    ../../../modules/home-manager/fish
    ../../../modules/home-manager/foot
    ../../../modules/home-manager/git
    ../../../modules/home-manager/gnupg
    ../../../modules/home-manager/man
    ../../../modules/home-manager/music
    ../../../modules/home-manager/pass
    ../../../modules/home-manager/photos
    ../../../modules/home-manager/video
    ../../../modules/home-manager/yubikey
  ];

  nixosModules = [
    emacs-config.nixosModules.nixos
    nixos-hardware.nixosModules.framework-12th-gen-intel

    ../../../modules/nixos/emacs
    ../../../modules/nixos/fish
    ../../../modules/nixos/fonts
    ../../../modules/nixos/kde
    ../../../modules/nixos/interception-tools
    ../../../modules/nixos/mullvad
    ../../../modules/nixos/networking
    ../../../modules/nixos/nix
    ../../../modules/nixos/sddm
    ../../../modules/nixos/steam
    ../../../modules/nixos/users
    ../../../modules/nixos/yubikey

    ({ pkgs
     , ... }:

       {
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
    homeManagerModules
    nixosModules;
}
