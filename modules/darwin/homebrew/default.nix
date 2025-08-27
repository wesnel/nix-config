{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.homebrew;
in {
  options.wgn.darwin.homebrew = {
    enable = mkEnableOption "Enables my Homebrew setup for Darwin";
  };

  config = mkIf cfg.enable {
    # TODO: nix-darwin has more options for homebrew which should be used.

    programs = {
      fish.interactiveShellInit =
        # HACK: I think Homebrew needs to be installed outside of Nix?
        lib.mkIf config.programs.fish.enable
        ''
          fish_add_path /opt/homebrew/bin
        '';
    };
  };
}
