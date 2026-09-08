{
  lib,
  pkgs,
  config,
  ...
}:
with lib; let
  cfg = config.wgn.home.sway;
in {
  options.wgn.home.sway = {
    enable = mkEnableOption "Enables my Sway setup for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs; [
      grim
      slurp
      wl-clipboard
    ];

    programs = {
      waybar = {
        enable = true;
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

      # https://github.com/nix-community/home-manager/issues/5311
      checkConfig = false;

      systemd = {
        enable = true;
      };

      config = let
        background-image = "${config.home.homeDirectory}/.background-image";

        foot = config.programs.foot;
        ghostty = config.programs.ghostty;

        terminal =
          if ghostty.enable
          then "${ghostty.package}/bin/ghostty"
          else if foot.enable
          then "${foot.package}/bin/foot"
          else null;
      in {
        menu = "${pkgs.albert}/bin/albert toggle";
        modifier = "Mod4";
        terminal = lib.mkIf (terminal != null) terminal;

        bars = [
          {
            command = "${config.programs.waybar.package}/bin/waybar";
          }
        ];

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
          {
            always = true;
            command = "${pkgs.albert}/bin/albert";
          }
        ];
      };

      extraSessionCommands = ''
        export MOZ_ENABLE_WAYLAND=1
        export XDG_CURRENT_DESKTOP="sway"
        export XDG_SESSION_TYPE="wayland"
      '';
    };
  };
}
