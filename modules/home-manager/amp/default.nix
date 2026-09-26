{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.amp;

  llm = import ../llm/lib.nix {inherit lib;};
in {
  options.wgn.home.amp = {
    enable = mkEnableOption "Enables my Amp skills for home-manager";
  };

  config = mkIf cfg.enable {
    wgn.home.llm.enable = true;

    # Amp reads global skills from ~/.config/agents/skills, which is shared
    # with other agents rather than being Amp's own directory. Its settings
    # and credentials live elsewhere, under ~/.config/amp and
    # ~/.local/share/amp, and are left to Amp.
    home.file = llm.mkSkillFiles ".config/agents/skills" config.wgn.home.llm.skills;
  };
}
