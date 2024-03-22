{ pkgs
, ... }:

{
  hardware.gpgSmartcards.enable = true;
  programs.ssh.startAgent = false;

  services = {
    pcscd.enable = true;
    udev.packages = with pkgs; [
      yubikey-personalization
      libu2f-host
    ];
  };
}
