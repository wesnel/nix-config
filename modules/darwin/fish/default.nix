{
  username,
  pkgs,
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.fish;
in {
  options.wgn.darwin.fish = {
    enable = mkEnableOption "Enables my Fish setup for Darwin";
  };

  config = mkIf cfg.enable {
    programs.fish = {
      enable = true;
    };

    environment = with pkgs; {
      shells = [fish];
    };

    users.users.${username} = {
      shell = config.programs.fish.package;
    };
  };
}
