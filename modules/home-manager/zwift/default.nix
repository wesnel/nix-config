{
  lib,
  pkgs,
  config,
  homeDirectory,
  username,
  ...
}: let
  inherit
    (lib)
    getExe
    mkEnableOption
    mkIf
    mkOption
    types
    ;

  cfg = config.wgn.home.zwift;
  class = "zwiftapp.exe";
in {
  options.wgn.home.zwift = {
    enable = mkEnableOption "Enables my Zwift setup for home-manager";

    workoutDir = mkOption rec {
      default = "${homeDirectory}/.config/zwift";
      example = default;
      description = "Zwift workout directory";
      type = types.str;
    };

    config = mkOption rec {
      default = "";
      example = default;
      description = "Zwift config file contents";
      type = types.lines;
    };

    userConfig = mkOption rec {
      default = "";
      example = default;
      description = "Zwift user config file contents";
      type = types.lines;
    };
  };

  config = let
    zwift-client = pkgs.writeShellApplication {
      name = "zwift";

      runtimeEnv = {
        CONTAINER_TOOL = null;
        DEBUG = "1";
        DONT_CHECK = "1";
        DONT_PULL = "1";
        WINE_EXPERIMENTAL_WAYLAND = "1";
        ZWIFT_FG = "1";
        ZWIFT_WORKOUT_DIR = cfg.workoutDir;
        IMAGE = "zwift-image";
        VERSION = "1.74.2";
      };

      excludeShellChecks = [
        "SC1090"
        "SC1091"
        "SC2001"
        "SC2034"
        "SC2046"
        "SC2054"
        "SC2068"
        "SC2086"
        "SC2191"
        "SC2206"
        "SC2207"
        "SC2229"
        "SC2162"
        "SC2143"
        "SC2181"
        "SC2236"
      ];

      runtimeInputs = with pkgs; [
        coreutils
        docker
        hostname
      ];

      text = let
        script = builtins.readFile "${pkgs.zwift-script}/bin/zwift.sh";
      in ''
        docker image load -i ${pkgs.zwift-image}

        ${script}
      '';
    };
  in
    mkIf cfg.enable {
      home = {
        file = {
          "zwift-config" = {
            text = ''
              ${cfg.config}
            '';

            target = ".config/zwift/config";
          };

          "zwift-user-config" = {
            text = ''
              ${cfg.userConfig}
            '';

            target = ".config/zwift/${username}-config";
          };
        };

        packages = [
          zwift-client
        ];
      };

      xdg.desktopEntries."${class}" = {
        name = "Zwift";
        icon = ./zwift.svg;
        exec = getExe zwift-client;
      };
    };
}
