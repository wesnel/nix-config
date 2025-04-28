{
  config,
  lib,
  pkgs,
  homeDirectory,
  ...
}:
with lib; let
  cfg = config.wgn.home.hyprland;
in {
  options.wgn.home.hyprland = {
    enable = mkEnableOption "Enables my Hyprland setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      fish
      foot
      grim
      rofi
      slurp
      wl-clipboard
    ];

    wayland.windowManager.hyprland = {
      enable = true;
      systemd.enableXdgAutostart = true;

      settings = {
        "$mod" = "SUPER";
        "$terminal" = "foot";
        "$menu" = "rofi -show run";

        monitor = ",preferred,auto,1";

        bind = [
          "$mod SHIFT, Q, killactive"

          "$mod, T, exec, $terminal"
          "$mod, D, exec, $menu"

          "$mod, left, movefocus, l"
          "$mod, right, movefocus, r"
          "$mod, up, movefocus, u"
          "$mod, down, movefocus, d"

          "$mod, 1, workspace, 1"
          "$mod, 2, workspace, 2"
          "$mod, 3, workspace, 3"
          "$mod, 4, workspace, 4"
          "$mod, 5, workspace, 5"
          "$mod, 6, workspace, 6"
          "$mod, 7, workspace, 7"
          "$mod, 8, workspace, 8"
          "$mod, 9, workspace, 9"
          "$mod, 0, workspace, 10"

          "$mod SHIFT, 1, movetoworkspace, 1"
          "$mod SHIFT, 2, movetoworkspace, 2"
          "$mod SHIFT, 3, movetoworkspace, 3"
          "$mod SHIFT, 4, movetoworkspace, 4"
          "$mod SHIFT, 5, movetoworkspace, 5"
          "$mod SHIFT, 6, movetoworkspace, 6"
          "$mod SHIFT, 7, movetoworkspace, 7"
          "$mod SHIFT, 8, movetoworkspace, 8"
          "$mod SHIFT, 9, movetoworkspace, 9"
          "$mod SHIFT, 0, movetoworkspace, 10"
        ];

        bindel = [
          ", XF86AudioRaiseVolume, exec, wpctl set-volume @DEFAULT_AUDIO_SINK@ 5%+"
          ", XF86AudioLowerVolume, exec, wpctl set-volume @DEFAULT_AUDIO_SINK@ 5%-"
          ", XF86AudioMute, exec, wpctl set-mute @DEFAULT_AUDIO_SINK@ toggle"
          ", XF86AudioMicMute, exec, wpctl set-mute @DEFAULT_AUDIO_SOURCE@ toggle"
          ", XF86AudioPlay, exec, playerctl play-pause"
          ", XF86AudioPause, exec, playerctl play-pause"
          ", XF86AudioNext, exec, playerctl next"
          ", XF86AudioPrev, exec, playerctl previous"

          ", XF86MonBrightnessUp, exec, brightnessctl s 10%+"
          ", XF86MonBrightnessDown, exec, brightnessctl s 10%-"
        ];

        misc = {
          force_default_wallpaper = 0;
          disable_hyprland_logo = false;
        };
      };
    };

    services = {
      playerctld.enable = true;

      hypridle = {
        enable = true;

        settings = {
          general = {
            before_sleep_cmd = "loginctl lock-session";
            after_sleep_cmd = "hyprctl dispatch dpms on";
            ignore_dbus_inhibit = false;
            lock_cmd = "pidof hyprlock || hyprlock";
          };

          listener = [
            {
              timeout = 150;
              on-timeout = "brightnessctl -s set 10";
              on-resume = "brightnessctl -r";
            }
            {
              timeout = 150;
              on-timeout = "brightnessctl -sd rgb:kbd_backlight set 0";
              on-resume = "brightnessctl -rd rgb:kbd_backlight";
            }
            {
              timeout = 300;
              on-timeout = "loginctl lock-session";
            }
            {
              timeout = 330;
              on-timeout = "hyprctl dispatch dpms off";
              on-resume = "hyprctl dispatch dpms on && brightnessctl -r";
            }
            {
              timeout = 1800;
              on-timeout = "systemctl suspend";
            }
          ];
        };
      };

      hyprpaper = {
        enable = true;

        settings = {
          ipc = "on";
          splash = false;
          splash_offset = 2.0;

          preload = [
            "${homeDirectory}/.background-image"
          ];

          wallpaper = [
            ",${homeDirectory}/.background-image"
          ];
        };
      };

      hyprsunset = {
        enable = true;

        transitions = {
          sunrise = {
            calendar = "*-*-* 06:00:00";
            requests = [
              ["temperature" "6500"]
              ["gamma 100"]
            ];
          };

          sunset = {
            calendar = "*-*-* 19:00:00";
            requests = [
              ["temperature" "3500"]
            ];
          };
        };
      };
    };

    programs = {
      hyprlock = {
        enable = true;

        settings = {
          general = {
            hide_cursor = true;
          };

          auth = {
            fingerprint.enabled = true;
          };

          background = [
            {
              path = "screenshot";
              blur_passes = 3;
              blur_size = 8;
            }
          ];

          input-field = [
            {
              size = "200, 50";
              position = "0, -80";
              monitor = "";
              dots_center = true;
              fade_on_empty = false;
              font_color = "rgb(202, 211, 245)";
              inner_color = "rgb(91, 96, 120)";
              outer_color = "rgb(24, 25, 38)";
              outline_thickness = 5;
              shadow_passes = 2;
            }
          ];
        };
      };

      fish.loginShellInit = ''
        uwsm check may-start && uwsm start select
      '';
    };

    home.sessionVariables.NIXOS_OZONE_WL = "1";
  };
}
