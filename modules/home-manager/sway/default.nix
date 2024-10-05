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
      in {
        menu = let
          foot = config.programs.foot.package;
        in
          lib.mkIf config.programs.foot.enable "exec ${foot}/bin/foot -a 'launcher' bash -c 'compgen -c | sort -u | ${pkgs.fzf}/bin/fzf | xargs -r swaymsg -t command exec'";
        modifier = "Mod4";
        terminal = lib.mkIf config.programs.foot.enable "foot";

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
  };
}
