{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.eca;

  llm = import ../llm/lib.nix {inherit lib;};
in {
  options.wgn.home.eca = {
    enable = mkEnableOption "Enables my Editor Code Assistant skills for home-manager";
  };

  config = mkIf cfg.enable {
    wgn.home.llm.enable = true;

    # Unlike the other agents, this server is pinned rather than left to
    # update itself: `eca-emacs' otherwise downloads its own copy from the
    # GitHub releases API on startup.
    home.packages = with pkgs; [
      eca
    ];

    # ECA discovers global skills under ~/.config/eca/skills rather than the
    # ~/.config/agents/skills that Amp reads, so this is its own directory
    # rather than a share.
    xdg.configFile = llm.mkSkillFiles "eca/skills" config.wgn.home.llm.skills;
  };
}
