{
  config,
  lib,
  homeDirectory,
  username,
  ...
}:
with lib; let
  cfg = config.wgn.darwin.paths;
in {
  options.wgn.darwin.paths = {
    enable = mkEnableOption "Enables my environment.etc.paths setup for Darwin";
  };

  config = mkIf cfg.enable {
    environment.etc."paths".text = ''
      ${homeDirectory}/.nix-profile/bin
      /etc/profiles/per-user/${username}/bin
      /run/current-system/sw/bin
      /nix/var/nix/profiles/default/bin
      /usr/local/bin
      /usr/bin
      /usr/sbin
      /bin
      /sbin
    '';
  };
}
