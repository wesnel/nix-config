{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.emacs;
in {
  options.wgn.home.emacs = {
    enable = mkEnableOption "Enables my Emacs setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs = {
      fish.interactiveShellInit =
        lib.mkIf config.programs.fish.enable
        ''
          set -gx EDITOR emacs
        '';
    };

    home = {
      programs.wgn.emacs = {
        enable = true;
        gnus.enable = true;
        # The notes repository the agents' plans are in, as org-roam's
        # directory, so each plan is a node beside the operator's notes.
        org-roam.directory = mkIf config.wgn.home.llm.enable config.wgn.home.llm.plans.repository.path;
        # The file the deployed agent-tools reads, whose config home is
        # baked into its build, so the plan views read the same one
        # whatever Emacs's environment says.
        agent-tools.configFile = mkIf config.wgn.home.llm.enable "${config.xdg.configHome}/agent-tools/config.json";
      };
    };
  };
}
