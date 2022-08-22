inputs@
{ pkgs
, ...
}:

{
  services.xserver = {
    enable = true;
    layout = "us";

    desktopManager.pantheon = {
      enable = true;
    };

    displayManager.lightdm = {
      enable = true;
      greeters.pantheon.enable = true;
    };
  };
}
