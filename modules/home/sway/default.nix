inputs@
{ config
, pkgs
, lib
, ...
}:

{
  imports = [
    ../programs/foot
    ../programs/mako
    ../services/gammastep
    ../services/kanshi
  ];

  home.packages = with pkgs; [
    grim
    slurp
    wl-clipboard
  ];

  wayland.windowManager.sway = {
    enable = true;
    systemdIntegration = true;

    config = let
      background-image = "${config.home.homeDirectory}/.background-image";
    in {
      menu = "exec foot -a 'launcher' bash -c 'compgen -c | sort -u | fzf | xargs -r swaymsg -t command exec'";
      modifier = "Mod4";
      terminal = "foot";

      input = {
        "*" = {
          xkb_options = "ctrl:nocaps";
        };
      };

      keybindings = let
        modifier = config.wayland.windowManager.sway.config.modifier;
      in lib.mkOptionDefault {
        "XF86Battery" = "exec --no-startup-id ${pkgs.swaylock}/bin/swaylock -elfF -s fill -i ${background-image}";

        # audio
        "XF86AudioRaiseVolume" = "exec --no-startup-id ${pkgs.pulseaudio-ctl}/bin/pulseaudio-ctl up";
        "XF86AudioLowerVolume" = "exec --no-startup-id ${pkgs.pulseaudio-ctl}/bin/pulseaudio-ctl down";
        "XF86AudioMute"        = "exec --no-startup-id ${pkgs.pulseaudio-ctl}/bin/pulseaudio-ctl mute";
        "XF86AudioMicMute"     = "exec --no-startup-id ${pkgs.pulseaudio-ctl}/bin/pulseaudio-ctl mute-input";

        # video
        "XF86MonBrightnessUp"   = "exec --no-startup-id ${pkgs.brightnessctl}/bin/brightnessctl set +100";
        "XF86MonBrightnessDown" = "exec --no-startup-id ${pkgs.brightnessctl}/bin/brightnessctl set 100-";

        # media
        "XF86AudioPlay"  = "exec --no-startup-id ${pkgs.mpc_cli}/bin/mpc toggle";
        "XF86AudioPause" = "exec --no-startup-id ${pkgs.mpc_cli}/bin/mpc toggle";
        "XF86AudioNext"  = "exec --no-startup-id ${pkgs.mpc_cli}/bin/mpc next";
        "XF86AudioPrev"  = "exec --no-startup-id ${pkgs.mpc_cli}/bin/mpc prev";

        # include workspace 10
        "${modifier}+0" = "workspace number 10";
        "${modifier}+Shift+0" = "move container to workspace number 10";
      };

      output = {
        "*" = {
          bg = "${background-image} fill";
        };
      };

      startup = [
        {
          always = true;
          command = ''
            ${pkgs.swayidle}/bin/swayidle -w \
              before-sleep '${pkgs.swaylock}/bin/swaylock \
                -elfF \
                -s fill \
                -i ${background-image}'
          '';
        }
      ];
    };

    extraConfig = ''
      for_window [app_id="^launcher$"] floating enable, border none, resize set width 25 ppt height 100 ppt, move position 0 px 0 px
    '';

    extraSessionCommands = ''
      export MOZ_ENABLE_WAYLAND=1
      export XDG_CURRENT_DESKTOP="sway"
      export XDG_SESSION_TYPE="wayland"
    '';
  };
}
