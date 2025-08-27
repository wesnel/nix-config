{
  config,
  lib,
  ...
}:
with lib; let
  cfg = config.wgn.home.ollama;
in {
  options.wgn.home.ollama = {
    enable = mkEnableOption "Enables my AI setup for home-manager";
  };

  # TODO: Configure AI packages in Emacs if that's enabled?
  config = mkIf cfg.enable {
    services.ollama = {
      enable = true;
    };

    programs.aichat = {
      enable = true;

      settings = {
        model = "ollama:mistral-small3.1:latest";

        clients = [
          {
            type = "openai-compatible";
            name = "ollama";
            api_base = "http://${config.services.ollama.host}:${builtins.toString config.services.ollama.port}/v1";
            # TODO: Add more models here depending on what's installed.
            models = [
              {
                name = "mistral-small3.1:latest";
                supports_function_calling = true;
                supports_vision = true;
              }
            ];
          }
        ];
      };
    };
  };
}
