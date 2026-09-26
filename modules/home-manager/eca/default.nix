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

    sandbox = {
      enable = mkEnableOption "Installs the wrapper that runs the server in a sandbox";

      backend = mkOption {
        type = types.enum ["gondolin" "bubblewrap"];
        default = "gondolin";

        description = ''
          Which isolation to use. Both answer to `eca-sandbox`, so a project's
          {file}`.dir-locals.el` is portable, but they are not equivalent:
          `gondolin` boots a micro-VM and enforces its egress allowlist at the
          network layer, while `bubblewrap` isolates only the filesystem and
          enforces egress through a proxy the server could step around. Pick
          `bubblewrap` for hosts without hardware virtualization.
        '';
      };
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
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
    }

    (mkIf (cfg.sandbox.enable && cfg.sandbox.backend == "gondolin") {
      home.packages = with pkgs; [
        eca-gondolin

        # The wrapper carries its own copy, but the CLI is what builds the
        # guest image, which is a manual per-machine step.
        gondolin
      ];
    })

    (mkIf (cfg.sandbox.enable && cfg.sandbox.backend == "bubblewrap") {
      home.packages = with pkgs; [
        eca-bwrap
      ];
    })
  ]);
}
