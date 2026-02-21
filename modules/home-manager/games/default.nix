{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.games;
in {
  options.wgn.home.games = {
    enable = mkEnableOption "Enables my games for home-manager";
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs;
      [
        forge-mtg
      ]
      ++ (optionals stdenv.isLinux [
        (dwarf-fortress-packages.dwarf-fortress-full.override (_: {
          enableDFHack = true;
        }))

        endless-sky
        openttd
        srb2
      ]);
  };
}
