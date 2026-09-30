{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.eca;

  llm = import ../llm/lib.nix {inherit lib;};
in {
  options.wgn.home.eca = {
    enable = mkEnableOption "Configure my Editor Code Assistant skills and agents";

    localModel = mkOption {
      type = types.nullOr types.str;
      default = null;
      example = "ollama/qwen2.5-coder:7b";

      description = ''
        Model backing the `local` agent, as `provider/model`. The model itself
        is pulled by hand; only the agent that selects it is declared here.

        It has to be one that calls tools natively. A model that instead
        describes the call in its reply -- which several coding models do,
        whatever capabilities they advertise -- leaves the agent with nothing
        to run, so it reads and edits nothing.

        Its context window and cost still have to be set under
        {option}`providers` in ECA's own config, which ECA owns: without them
        it treats the window as unbounded and never auto-compacts.
      '';
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      wgn.home.llm.enable = true;

      # ECA discovers global skills under ~/.config/eca/skills rather than the
      # ~/.config/agents/skills that Amp reads, so this is its own directory
      # rather than a share.
      xdg.configFile =
        (llm.mkSkillFiles "eca/skills" config.wgn.home.llm.skills)
        // {
          # Which skills the wrapper may carry into the guest. It is a list
          # beside the skills rather than a mark inside each one, because a
          # skill directory is shared with the other agents and they have no
          # use for it -- and because home-manager links such a directory
          # whole, leaving nowhere to put a file the set does not declare.
          "eca/sandbox-skills.json".text =
            builtins.toJSON
            (llm.sandboxSkillNames config.wgn.home.llm.skills);
        };
    }

    # ~/.config/eca/agents is read and never written by ECA, so an agent can
    # be declared here without taking over the config file ECA writes itself.
    (mkIf (cfg.localModel != null) {
      # Not `explorer', the read-only agent the planner delegates
      # investigation to: an agent inheriting it cannot change a file.
      xdg.configFile."eca/agents/local.md".text = ''
        ---
        inherit: code
        description: Works in the codebase using a model running on this machine
        model: ${cfg.localModel}
        ---
      '';
    })
  ]);
}
