{ lib
, pkgs
, config
, ... }:

{
  home.packages = with pkgs; [
    grim
    slurp
    wl-clipboard
  ];

  programs = {
    foot = {
      enable = true;

      settings = {
        colors = {
          foreground = "b4abac";
          background = "090a18";
          regular0 = "090a18";  # black
          regular1 = "ff778a";  # red
          regular2 = "6ab539";  # green
          regular3 = "bfa01a";  # yellow
          regular4 = "4aaed3";  # blue
          regular5 = "e58a82";  # magenta
          regular6 = "29b3bb";  # cyan
          regular7 = "a59ebd";  # white
          bright0 = "260e22";   # bright black
          bright1 = "f78e2f";   # bright red
          bright2 = "60ba80";   # bright green
          bright3 = "de9b1d";   # bright yellow
          bright4 = "8ba7ea";   # bright blue
          bright5 = "e08bd6";   # bright magenta
          bright6 = "2cbab6";   # bright cyan
          bright7 = "b4abac";   # bright white
        };

        cursor = {
          color = "090a18 b4abac";
        };
      };
    };
  };

  services = {
    gammastep = {
      enable = true;
      latitude = "33.522861";
      longitude = "-86.807701";
    };

    kanshi.enable = true;
    mako.enable = true;
  };

  wayland.windowManager.sway = {
    enable = true;
    systemdIntegration = true;

    config = let
      background-image = "${config.home.homeDirectory}/.background-image";

      foot = config.programs.foot.package;
    in {
      menu = "exec ${foot}/bin/foot -a 'launcher' bash -c 'compgen -c | sort -u | ${pkgs.fzf}/bin/fzf | xargs -r swaymsg -t command exec'";
      modifier = "Mod4";
      terminal = "foot";

      input = {
        "*" = {
          xkb_options = "ctrl:nocaps";
        };
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
