{ pkgs
, ... }:

{
  # swaywm/sway/issues/2773
  security.pam.services.swaylock = {
    text = ''
      auth include login
    '';
  };

  services.xserver = {
    enable = true;

    displayManager = {
      sessionPackages = with pkgs; [
        sway
      ];

      sddm.enable = true;
    };
  };

  # other wayland things are managed by home-manager.
}
