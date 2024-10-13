{
  lib,
  pkgs,
  config,
  homeDirectory,
  username,
  ...
}:
with lib; let
  cfg = config.wgn.home.zwift;
  class = "zwiftapp.exe";

  zwift-offline = let
    imageDigest = "sha256:74d461d0271c1b11520a3d8aa8973dc9254fcecd2ef07c7ed53c618a741279b0";
    sha256 = "0q97352z34mjmximiqhirn2pzrljkiiw07cshp9yvzr69kj2aqa3";
  in
    pkgs.dockerTools.pullImage {
      imageName = "zoffline/zoffline";
      finalImageName = "zoffline";
      finalImageTag = "zoffline_1.0.135674";

      inherit
        imageDigest
        sha256
        ;
    };

  zwift-image = pkgs.dockerTools.buildLayeredImage {
    name = "zwift";
    tag = "1.74.2";

    fromImage = let
      imageDigest = "sha256:27259b3f79e0270b36bef093dcf22189c5e2e86bf05e5de4332bf1f561ca3b8f";
      sha256 = "sha256-Jlzrl8LhU0R2J00JmTWVCtvM42oQSra5ImGNCw+R2yU=";
    in
      pkgs.dockerTools.pullImage {
        imageName = "netbrain/zwift";
        finalImageName = "zwift";
        finalImageTag = "1.74.2";

        inherit
          imageDigest
          sha256
          ;
      };

    # TODO: If cfg.offline, modify the fromImage to make the necessary
    #       changes for zwift-offline to work.
  };

  zwift-client = let
    zwift-script = let
      rev = "ac8eb25cebc2714e8df779d001a05ec8b725ddad";
      hash = "sha256-oNo2dOw3R/pd3IhVbT2AbG709SUIHaqbkTTJ57vO56c=";
    in
      pkgs.stdenv.mkDerivation {
        name = "zwift";

        src = pkgs.fetchurl {
          url = "https://raw.githubusercontent.com/netbrain/zwift/${rev}/zwift.sh";
          inherit hash;
        };

        buildInputs = with pkgs; [
          bash
        ];

        runtimeInputs = [
          zwift-image
        ];

        unpackPhase = ''
          runHook preUnpack

          mkdir -p $out/bin
          cp $src $out/bin/zwift

          runHook postUnpack
        '';

        installPhase = ''
          runHook preInstall

          chmod +x $out/bin/zwift

          runHook postInstall
        '';
      };
  in
    pkgs.writeShellApplication {
      name = "zwift";

      runtimeEnv = {
        CONTAINER_TOOL = null;
        DEBUG = "1";
        DONT_CHECK = "1";
        DONT_PULL = "1";
        WINE_EXPERIMENTAL_WAYLAND = null;
        ZWIFT_FG = "1";
        ZWIFT_WORKOUT_DIR = cfg.workoutDir;
        IMAGE = "zwift";
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
        podman
        hostname
      ];

      text = builtins.readFile "${zwift-script}/bin/zwift";
    };
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

    offline = mkEnableOption "Enable Zwift offline";
  };

  config = mkIf cfg.enable {
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
