{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.colima;
in {
  options.wgn.home.colima = {
    enable = mkEnableOption "Colima, run as a user service, and the docker client that talks to it";
  };

  config = mkIf cfg.enable {
    # Home Manager's default profile starts at login and activates its docker
    # context. programs.docker-cli stays off: it would link
    # ~/.docker/config.json read-only from the store, where `docker login`
    # and colima's own `docker context use` both write.
    services.colima.enable = true;

    home.packages = [config.services.colima.dockerPackage];
  };
}
