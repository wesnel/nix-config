{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.homebrew;
in {
  options.wgn.darwin.homebrew = {
    # HACK: Homebrew itself needs to be installed outside of Nix.
    enable = mkEnableOption "Enables my Homebrew setup for Darwin";
  };

  config = mkIf cfg.enable {
    homebrew = {
      enable = true;
      enableFishIntegration = config.programs.fish.enable;

      casks = [
        "stats"
      ];
    };
  };
}
