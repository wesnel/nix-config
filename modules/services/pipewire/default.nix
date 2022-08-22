inputs@
{ config
, pkgs
, lib
, ...
}:

{
  hardware.pulseaudio.enable = false;

  services.pipewire = {
    enable = true;

    alsa = {
      enable = true;
      support32Bit = true;
    };

    pulse.enable = true;
  };

  security.rtkit.enable = true;
  sound.enable = true;
}
