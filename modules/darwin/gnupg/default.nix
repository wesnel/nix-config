{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.gnupg;
in {
  options.wgn.darwin.gnupg = {
    enable = mkEnableOption "Enables my GnuPG setup for Darwin";
  };

  config = mkIf cfg.enable {
    programs.gnupg = {
      agent = {
        enable = true;
        enableSSHSupport = true;
      };
    };
  };
}
