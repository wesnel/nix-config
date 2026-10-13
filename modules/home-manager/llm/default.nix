{
  config,
  emacs-skills,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.llm;

  # d2 renders a PNG through a Chromium it downloads at run time, after asking
  # on the terminal, which an agent cannot answer and a sandbox cannot
  # download. So a PNG is rendered as SVG, which needs no browser, and
  # converted.
  d2Png = pkgs.writeShellApplication {
    name = "d2";
    runtimeInputs = [pkgs.d2 pkgs.librsvg];
    text = ''
      out="''${*: -1}"
      if [[ $# -lt 2 || "$out" != *.png ]]; then
        exec d2 "$@"
      fi
      tmp="$(mktemp -d)"
      trap 'rm -rf "$tmp"' EXIT
      d2 "''${@:1:$#-1}" "$tmp/diagram.svg"
      rsvg-convert -f png -o "$out" "$tmp/diagram.svg"
    '';
  };
in {
  options.wgn.home.llm = {
    enable = mkEnableOption "Enables the plumbing shared by my coding agents";

    instructions = mkOption {
      type = types.path;
      default = ./AGENTS.md;

      description = ''
        Working agreements every enabled coding agent loads into each session,
        linked to wherever that agent reads its user-level instructions.
      '';
    };

    skills = mkOption {
      type = with types;
        attrsOf (
          coercedTo (either path lines)
          (content:
            if isPath content
            then {source = content;}
            else {text = content;})
          (submodule {
            options = {
              source = mkOption {
                type = nullOr path;
                default = null;

                description = ''
                  File linked in as the skill's SKILL.md, or the skill's
                  whole directory when the skill keeps scripts or Emacs Lisp
                  beside it. Mutually exclusive with {option}`text`.
                '';
              };

              text = mkOption {
                type = nullOr lines;
                default = null;

                description = ''
                  Skill written verbatim. Mutually exclusive with
                  {option}`source`.
                '';
              };

              sandbox = mkOption {
                type = bool;
                default = false;

                description = ''
                  Whether the skill works with nothing but the workspace and
                  the sandbox's own tools.

                  Off by default, because most of these drive something on
                  this machine -- an editor, a notifier, a command installed
                  here -- and none of it exists in the guest. Such a skill is
                  worse there than missing: it is offered to the agent, which
                  spends a turn calling it and gets nothing back.
                '';
              };
            };
          })
        );

      description = ''
        Skills offered to every enabled coding agent. A path is linked into
        the agent's skill directory; a string is written there verbatim.

        Give {option}`sandbox` instead of a bare path or string to say the
        skill may also be carried into a sandboxed session.
      '';

      default =
        {
          notify = ./skills/notify/SKILL.md;

        }
        # Whole directories: most of these load Emacs Lisp or run a script
        # that sits beside their SKILL.md. d2 and github-activity run only
        # when invoked by name.
        // genAttrs [
          "d2"
          "describe"
          "dired"
          "emacsclient"
          "file-links"
          "github-activity"
          "highlight"
          "open"
          "select"
        ] (name: {source = "${emacs-skills}/skills/${name}";})
        // optionalAttrs pkgs.stdenv.hostPlatform.isDarwin {
          trash = ./skills/trash/SKILL.md;
        }
        // {
          signing-pass = {
            source = ./skills/signing-pass/SKILL.md;
            sandbox = true;
          };
          restack-branches = {
            source = ./skills/restack-branches/SKILL.md;
            sandbox = true;
          };
        };
    };
  };

  config = mkIf cfg.enable {
    home.packages = with pkgs;
      [
        # For the d2 and github-activity skills; the latter fetches
        # matplotlib through uv when it runs.
        d2Png
        uv
      ]
      # Every agent is told to leave signing to `git sign-pass`, which this
      # installs. These run the git and gh on PATH, so the user's signing and
      # SSH settings apply.
      ++ [agent-tools];
  };
}
