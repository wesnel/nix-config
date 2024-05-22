_:

{
  services = {
    desktopManager = {
      plasma6.enable = true;
    };

    displayManager = {
      defaultSession = "plasma";

      sddm = {
        enable = true;
        wayland.enable = true;
      };
    };
  };

  programs.dconf.enable = true;
}
