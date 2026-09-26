{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.docs-mcp-server;

  # Unified mode serves the MCP endpoint, the dashboard and the scraping
  # worker from one process, which is what lets a single index be shared.
  args = [
    "-y"
    "@arabold/docs-mcp-server@latest"
    "server"
    "--protocol"
    "http"

    # Reached from the sandbox through a mapping the wrapper makes, which
    # dials from this machine, so the port itself never has to be offered
    # to the network.
    "--host"
    "127.0.0.1"
    "--port"
    (toString cfg.port)
    "--store-path"
    cfg.storePath

    # Scraping a large site outlives a login session, and the index is only
    # as good as the jobs that finished.
    "--resume"
    "--no-logo"
  ];

  environment =
    {
      DOCS_MCP_TELEMETRY = "false";
      DOCS_MCP_EMBEDDING_MODEL = cfg.embeddingModel;
      OPENAI_API_BASE = cfg.embeddingApiBase;

      # Unused by a local model server, but the client refuses to construct
      # itself without one.
      OPENAI_API_KEY = "unused";

      # The package's entry point is `#!/usr/bin/env node', so node has to be
      # findable by name. A service inherits none of the login shell's path,
      # and npx resolving through its own absolute shebang is not enough to
      # carry the interpreter through to what it then runs.
      PATH = "${pkgs.nodejs}/bin:/usr/bin:/bin";
    }
    // optionalAttrs (cfg.vectorDimension != null) {
      DOCS_MCP_EMBEDDINGS_VECTOR_DIMENSION = toString cfg.vectorDimension;
    };
in {
  options.wgn.home.docs-mcp-server = {
    enable = mkEnableOption "Runs the documentation index as a service for every client to share";

    port = mkOption {
      type = types.port;
      default = 6280;

      description = ''
        Port the MCP endpoint and dashboard are served on.
      '';
    };

    storePath = mkOption {
      type = types.str;
      default = "${config.xdg.dataHome}/docs-mcp-server";
      defaultText = literalExpression ''"''${config.xdg.dataHome}/docs-mcp-server"'';

      description = ''
        Directory holding the index. One writer at a time: a client that
        starts its own copy of the server against this path will contend for
        the same database.
      '';
    };

    embeddingModel = mkOption {
      type = types.str;
      default = "openai:nomic-embed-text";

      description = ''
        Model used to embed scraped pages, as `provider:model`. A server
        speaking the OpenAI API is addressed through the `openai` provider
        whoever serves it, so a local one is named this way too.

        Changing it invalidates the index: vectors made by another model are
        not comparable with the ones this model produces.
      '';
    };

    embeddingApiBase = mkOption {
      type = types.str;
      default = "http://127.0.0.1:11434/v1";

      description = ''
        Endpoint offering the embedding model, which is what points the
        `openai` provider at a server on this machine instead of OpenAI.
      '';
    };

    vectorDimension = mkOption {
      type = types.nullOr types.ints.positive;
      default = 768;
      example = 1536;

      description = ''
        Width of the vectors {option}`embeddingModel` returns. The sizes of
        OpenAI's own models are known, so this is what a model served locally
        needs instead.
      '';
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      # Fetched at startup rather than pinned, because the index it builds is
      # already tied to a scraper version rather than to this machine.
      home.packages = with pkgs; [
        nodejs
      ];
    }

    (mkIf pkgs.stdenv.hostPlatform.isDarwin {
      launchd.agents.docs-mcp-server = {
        enable = true;

        # Nothing here needs the Aqua session, and the index should be warm
        # before a graphical login rather than because of one.
        domain = "user";

        config = {
          ProgramArguments = ["${pkgs.nodejs}/bin/npx"] ++ args;
          EnvironmentVariables = environment;
          RunAtLoad = true;
          KeepAlive = true;

          # Without these the only evidence of a failed start is an exit code
          # from `launchctl print', which does not say what went wrong.
          StandardOutPath = "${config.xdg.stateHome}/docs-mcp-server.log";
          StandardErrorPath = "${config.xdg.stateHome}/docs-mcp-server.log";
        };
      };
    })

    (mkIf pkgs.stdenv.hostPlatform.isLinux {
      systemd.user.services.docs-mcp-server = {
        Unit = {
          Description = "Documentation index served over MCP";
          After = ["network.target"];
        };

        Service = {
          ExecStart = concatStringsSep " " (["${pkgs.nodejs}/bin/npx"] ++ map escapeShellArg args);
          Environment = mapAttrsToList (name: value: "${name}=${value}") environment;
          Restart = "always";
        };

        Install = {
          WantedBy = ["default.target"];
        };
      };
    })
  ]);
}
