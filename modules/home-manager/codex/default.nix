{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.codex;

  llm = import ../llm/lib.nix {inherit lib;};
in {
  options.wgn.home.codex = {
    enable = mkEnableOption "Enables my Codex skills for home-manager";
  };

  config = mkIf cfg.enable {
    wgn.home.llm.enable = true;

    # Codex is installed and updated by itself, and writes ~/.codex/config.toml
    # to record project trust, MCP servers and model choice. Generating that
    # file is what stops it being able to.
    home.file = llm.mkSkillFiles ".codex/skills" config.wgn.home.llm.skills;
  };
}
