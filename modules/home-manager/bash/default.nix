{
  config,
  lib,
  homeDirectory,
  ...
}:
with lib; let
  cfg = config.wgn.home.bash;
in {
  options.wgn.home.bash = {
    enable = mkEnableOption "Enables my Bash setup for home-manager";
  };

  config = mkIf cfg.enable {
    programs.bash = {
      enable = true;

      initExtra = ''
        case ":$PATH:" in
          *":${homeDirectory}/.local/bin:"*) ;;
          *) export PATH="${homeDirectory}/.local/bin:$PATH" ;;
        esac
      '';
    };
  };
}
