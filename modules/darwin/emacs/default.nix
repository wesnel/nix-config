{
  pkgs,
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.emacs;
in {
  options.wgn.darwin.emacs = {
    enable = mkEnableOption "Enables my Emacs setup for Darwin";
  };

  config = mkIf cfg.enable {
    environment.systemPackages = [
      config.programs.wgn.emacs.package
    ];

    programs = {
      wgn.emacs = {
        enable = true;
        package = mkDefault pkgs.wgn-emacs-macport;
      };
    };

    services.emacs = {
      enable = true;
    };
  };
}
