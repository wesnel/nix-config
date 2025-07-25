{
  pkgs,
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.emacs;

  openNewEmacsAppScriptName = "open-new-emacs-app";

  openNewEmacsApp = pkgs.writeShellScriptBin openNewEmacsAppScriptName ''
    open -a "/Applications/Nix Apps/Emacs.app" -nW --args --chdir $PWD --no-splash "$@"
  '';
in {
  options.wgn.darwin.emacs = {
    enable = mkEnableOption "Enables my Emacs setup for Darwin";
  };

  config = mkIf cfg.enable {
    environment.systemPackages = [
      config.programs.wgn.emacs.package
      openNewEmacsApp
    ];

    programs = {
      fish.interactiveShellInit =
        lib.mkIf config.programs.fish.enable
        ''
          # ${openNewEmacsAppScriptName} must be on $PATH in order for platformctl to recognize it as a valid
          # command.  It opens a completely new GUI Emacs App instance and waits for
          # it to exit.  I would prefer to open a new frame of the existing GUI Emacs
          # App instance (if one exists), but I cannot figure this out.  When I try,
          # I end up either opening a terminal instance of the Emacs client, or
          # platformctl thinks that the command exits instantly as soon as a new
          # Emacs buffer is created in the daemon.
          set -gx VISUAL ${openNewEmacsApp}/bin/${openNewEmacsAppScriptName}
        '';

      wgn.emacs = {
        enable = true;
        package = pkgs.wgn-emacs-macport;
      };
    };

    services.emacs = {
      enable = true;
    };
  };
}
