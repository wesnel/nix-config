{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.pass;
in {
  options.wgn.home.pass = {
    enable = mkEnableOption "Enables my Pass setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs.password-store = {
      enable = true;

      package = pkgs.pass.withExtensions (exts:
        with exts; [
          pass-update
        ]);
    };
  };
}
