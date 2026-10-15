{
  config,
  lib,
  pkgs,
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
    home.file =
      llm.mkSkillFiles pkgs ".config/agents/skills" config.wgn.home.llm.skills
      // {
        # Amp also reads ~/.config/AGENTS.md; this is the one it calls its own.
        ".config/amp/AGENTS.md".source = config.wgn.home.llm.instructions;
      };
  };
}
