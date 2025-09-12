{
  config,
  lib,
  pkgs,
  homeDirectory,
  ...
}:
with lib; let
  cfg = config.wgn.home.fish;
in {
  options.wgn.home.fish = {
    enable = mkEnableOption "Enables my Fish setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs.fish = {
      enable = true;

      interactiveShellInit = lib.mkIf config.programs.fish.enable ''
        fish_add_path ${homeDirectory}/.local/bin
      '';
    };

    home.shellAliases = let
      e = "$EDITOR";
      ls = "${pkgs.eza}/bin/eza";
      l = "${ls} -laF --color=always --color-scale --icons --git";
      tree = "${ls} -T";
      t = "${l} -T";
    in {
      inherit
        e
        ls
        l
        tree
        t
        ;
    };
  };
}
