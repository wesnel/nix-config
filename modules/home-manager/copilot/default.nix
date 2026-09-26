{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.copilot;

  llm = import ../llm/lib.nix {inherit lib;};
in {
  options.wgn.home.copilot = {
    enable = mkEnableOption "Enables my GitHub Copilot CLI skills for home-manager";
  };

  config = mkIf cfg.enable {
    wgn.home.llm.enable = true;

    home.file = llm.mkSkillFiles ".copilot/skills" config.wgn.home.llm.skills;
  };
}
