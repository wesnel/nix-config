{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.zen;
in {
  options.wgn.darwin.zen = {
    # A Homebrew cask, so the machine needs wgn.darwin.homebrew too.
    enable = mkEnableOption "Enables my Zen browser setup for Darwin";
  };

  config = mkIf cfg.enable {
    homebrew.casks = [
      "zen"
    ];
  };
}
