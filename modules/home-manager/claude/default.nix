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
      llm.mkSkillFiles ".claude/skills" config.wgn.home.llm.skills
      // {
        ".claude/CLAUDE.md".source = config.wgn.home.llm.instructions;
      };
  };
}
