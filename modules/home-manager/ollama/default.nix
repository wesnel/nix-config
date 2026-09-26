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

    contextLength = mkOption {
      type = types.nullOr types.ints.positive;
      default = null;
      example = 32768;

      description = ''
        Context window the server offers, in tokens. Left unset it is chosen
        from available memory and so differs between machines, which would
        let a client believe it has more room than the server will give it
        and see its prompt truncated instead of compacted. Set it to the same
        figure the client is told.
      '';
    };
  };

  config = mkIf cfg.enable {
    # Models are pulled by hand rather than declared: they are gigabytes
    # apiece and belong in the server's own store, not in a closure.
    #
    # `acceleration` is left alone, since its values select between Linux GPU
    # backends and Metal needs no selection.
    services.ollama = {
      enable = true;

      environmentVariables = mkIf (cfg.contextLength != null) {
        OLLAMA_CONTEXT_LENGTH = toString cfg.contextLength;
      };
    };
  };
}
