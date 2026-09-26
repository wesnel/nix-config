{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.ollama;
in {
  options.wgn.home.ollama = {
    enable = mkEnableOption "Enables my local model server for home-manager";
  };

  config = mkIf cfg.enable {
    # Models are pulled by hand rather than declared: they are gigabytes
    # apiece and belong in the server's own store, not in a closure.
    #
    # `acceleration` is left alone, since its values select between Linux GPU
    # backends and Metal needs no selection.
    services.ollama = {
      enable = true;
    };
  };
}
