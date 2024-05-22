_:

{
  services = {
    displayManager = {
      defaultSession = "plasma";

      sddm = {
        enable = true;
        wayland.enable = true;
      };
    };

    xserver = {
      enable = true;

      desktopManager.plasma6.enable = true;
    };
  };

  programs.dconf.enable = true;
}
