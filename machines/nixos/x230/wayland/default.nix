{ lib
, config
, pkgs
, ... }:

{
  wayland.windowManager.sway = {
    config = {
      keybindings = let
        modifier = config.wayland.windowManager.sway.config.modifier;
        background-image = "${config.home.homeDirectory}/.background-image";
      in lib.mkOptionDefault {
        "XF86Battery" = "exec --no-startup-id ${pkgs.swaylock}/bin/swaylock -elfF -s fill -i ${background-image}";

        # audio
        "XF86AudioRaiseVolume" = "exec --no-startup-id ${pkgs.pulseaudio-ctl}/bin/pulseaudio-ctl up";
        "XF86AudioLowerVolume" = "exec --no-startup-id ${pkgs.pulseaudio-ctl}/bin/pulseaudio-ctl down";
        "XF86AudioMute"        = "exec --no-startup-id ${pkgs.pulseaudio-ctl}/bin/pulseaudio-ctl mute";
        "XF86AudioMicMute"     = "exec --no-startup-id ${pkgs.pulseaudio-ctl}/bin/pulseaudio-ctl mute-input";

        # video
        "XF86MonBrightnessUp"   = "exec --no-startup-id ${pkgs.brightnessctl}/bin/brightnessctl set +5%";
        "XF86MonBrightnessDown" = "exec --no-startup-id ${pkgs.brightnessctl}/bin/brightnessctl set 5%-";

        # media
        "XF86AudioPlay"  = "exec --no-startup-id ${pkgs.mpc_cli}/bin/mpc toggle";
        "XF86AudioPause" = "exec --no-startup-id ${pkgs.mpc_cli}/bin/mpc toggle";
        "XF86AudioNext"  = "exec --no-startup-id ${pkgs.mpc_cli}/bin/mpc next";
        "XF86AudioPrev"  = "exec --no-startup-id ${pkgs.mpc_cli}/bin/mpc prev";

        # include workspace 10
        "${modifier}+0" = "workspace number 10";
        "${modifier}+Shift+0" = "move container to workspace number 10";
      };
    };
  };
}
