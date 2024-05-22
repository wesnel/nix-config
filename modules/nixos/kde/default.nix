_:

{
  services = {
    displayManager.sddm.enable = true;

    xserver = {
      enable = true;

      desktopManager.plasma5.enable = true;
    };
  };

  programs.dconf.enable = true;
}
