{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.claude;

  llm = import ../llm/lib.nix {inherit lib;};
in {
  options.wgn.home.claude = {
    enable = mkEnableOption "Enables my Claude Code skills and context for home-manager";
  };

  config = mkIf cfg.enable {
    wgn.home.llm.enable = true;

    # Claude Code is installed and updated by itself, and owns ~/.claude.json
    # and ~/.claude/settings.json. Only the files it reads without writing
    # are kept here.
    home.file =
      llm.mkSkillFiles ".claude/skills" (
        config.wgn.home.llm.skills
        // {
          restack-branches = ./skills/restack-branches/SKILL.md;
        }
      )
      // {
        ".claude/CLAUDE.md".source = ./CLAUDE.md;
      };
  };
}
