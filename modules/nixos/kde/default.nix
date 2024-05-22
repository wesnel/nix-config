_:

{
  services = {
    displayManager.sddm.enable = true;

    xserver = {
      enable = true;

      desktopManager.plasma6.enable = true;
    };
  };

  programs.dconf.enable = true;
}
