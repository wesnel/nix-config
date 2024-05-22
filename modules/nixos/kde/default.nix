_:

{
  services = {
    desktopManager = {
      plasma6.enable = true;
    };

    displayManager = {
      defaultSession = "plasma";
    };
  };

  programs.dconf.enable = true;
}
