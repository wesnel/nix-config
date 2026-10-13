{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.claude;

  llm = import ../llm/lib.nix {inherit lib;};
in {
  options.wgn.home.claude = {
    enable = mkEnableOption "Enables my Claude Code skills and context for home-manager";

    settings = mkOption {
      type = types.attrs;

      description = ''
        Keys merged into ~/.claude/settings.json on every switch. The file stays
        Claude Code's, so settings changed from inside it are kept, but a key
        set here returns to this value at the next switch.
      '';

      default = {
        # Shared instructions forbid attribution trailers; this stops Claude
        # Code adding them, in the form every version accepts.
        attribution = {
          commit = "";
          pr = "";
          sessionUrl = false;
        };
      };
    };
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

    # Merged rather than linked: a read-only settings.json would make every
    # change made from inside Claude Code last only for that session.
    home.activation.claudeSettings = lib.hm.dag.entryAfter ["writeBoundary"] ''
      settings="$HOME/.claude/settings.json"
      patch=${escapeShellArg (builtins.toJSON cfg.settings)}

      if [ -L "$settings" ]; then
        warnEcho "$settings is a link, so the declared Claude Code settings were not merged into it."
      elif [ ! -e "$settings" ]; then
        if [ -z "''${DRY_RUN:-}" ]; then
          mkdir -p "$(dirname "$settings")"
          ${pkgs.jq}/bin/jq -n --argjson patch "$patch" '$patch' > "$settings"
        fi
      elif merged=$(${pkgs.jq}/bin/jq --argjson patch "$patch" '. * $patch' "$settings"); then
        if [ "$merged" != "$(cat "$settings")" ]; then
          run cp "$settings" "$settings.before-home-manager"
          [ -n "''${DRY_RUN:-}" ] || printf '%s\n' "$merged" > "$settings"
        fi
      else
        warnEcho "$settings is not valid JSON, so the declared Claude Code settings were not merged into it."
      fi
    '';
  };
}
