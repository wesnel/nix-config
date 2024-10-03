{ nixos-hardware
, emacs-config }:

let
  computerName = "ingot";
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
    ../../../modules/home-manager/gamedev
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

    ../../../modules/nixos/emacs
    ../../../modules/nixos/fish
    ../../../modules/nixos/fonts
    ../../../modules/nixos/kde
    ../../../modules/nixos/mullvad
    ../../../modules/nixos/networking
    ../../../modules/nixos/nix
    ../../../modules/nixos/sddm
    ../../../modules/nixos/steam
    ../../../modules/nixos/users
    ../../../modules/nixos/virtualisation
    ../../../modules/nixos/yubikey

    ({ pkgs
     , config
     , ... }:

       {
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
    homeManagerModules
    nixosModules;
}
