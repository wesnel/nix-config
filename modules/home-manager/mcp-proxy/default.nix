{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.mcp-proxy;
  docs = config.wgn.home.docs-mcp-server;
  darwin = pkgs.stdenv.hostPlatform.isDarwin;

  proxy = getExe pkgs.tbxark-mcp-proxy;
  label = "org.nix-community.home.mcp-proxy";
  tokenFile = "${cfg.stateDir}/token";

  # Remote servers are reached with short-lived OAuth tokens that the proxy
  # obtains and refreshes itself; nothing here is a credential.
  oauthServer = url: {
    inherit url;
    transportType = "streamable-http";
    oauth.scopes = ["read"];
  };

  servers =
    optionalAttrs cfg.linear {
      # Linear's read-only endpoint exposes only read tools whatever the
      # token's scope.
      linear = oauthServer "https://mcp.linear.app/mcp/readonly";
    }
    // optionalAttrs cfg.buildkite {
      buildkite = oauthServer "https://mcp.buildkite.com/mcp/readonly";
    }
    // optionalAttrs docs.enable {
      docs = {
        url = "http://127.0.0.1:${toString docs.port}/mcp";
        transportType = "streamable-http";
        # Allow, never block: anything the server adds stays hidden. fetch_url
        # and scrape_docs fetch arbitrary URLs, and the rest change the index.
        # An allow list must not be empty, which exposes every tool.
        options.toolFilter = {
          mode = "allow";
          list = ["search_docs" "list_libraries" "find_version" "list_jobs" "get_job_info"];
        };
      };
    };

  configFile = pkgs.writeText "mcp-proxy.json" (builtins.toJSON {
    mcpProxy = {
      baseURL = "http://127.0.0.1:${toString cfg.port}";
      addr = "127.0.0.1:${toString cfg.port}";
      name = "mcp-proxy";
      version = "1";
      type = "streamable-http";
      options = {
        # Expanded from the environment by the proxy at start.
        authTokens = ["\${MCP_PROXY_TOKEN}"];
        # A server that is not authorized yet must not stop the others.
        panicIfInvalid = false;
        autoReconnect = true;
        logEnabled = true;
      };
    };
    mcpServers = servers;
  });

  serve = pkgs.writeShellApplication {
    name = "mcp-proxy-serve";
    text = ''
      MCP_PROXY_TOKEN="$(cat ${escapeShellArg tokenFile})"
      export MCP_PROXY_TOKEN
      exec ${proxy} -config ${configFile}
    '';
  };

  restart =
    if darwin
    then "launchctl kickstart -k \"user/$(id -u)/${label}\""
    else "systemctl --user restart mcp-proxy.service";

  authorize = pkgs.writeShellApplication {
    name = "mcp-proxy-authorize";
    text = ''
      if [[ $# -ne 1 ]]; then
        echo "usage: mcp-proxy-authorize SERVER   (one of: ${concatStringsSep ", " (attrNames (filterAttrs (_: s: s ? oauth) servers))})" >&2
        exit 2
      fi
      # The token itself is not needed to authorize, but the config refers to it.
      MCP_PROXY_TOKEN=unused ${proxy} -config ${configFile} -authorize "$1"
      # A route is mounted only when its server connects, so the running
      # proxy has to start again to use the new token.
      ${restart}
    '';
  };

  # Prints the header every client sends, reading the token when the client
  # connects so that it is never copied into a client's own config.
  headersHelper = pkgs.writeShellScript "mcp-proxy-headers" ''
    printf '{"Authorization":"Bearer %s"}\n' "$(cat ${escapeShellArg tokenFile})"
  '';

  status = pkgs.writeShellApplication {
    name = "mcp-proxy-status";
    text = ''
      MCP_PROXY_TOKEN=unused exec ${proxy} -config ${configFile} -auth-status "$@"
    '';
  };
in {
  options.wgn.home.mcp-proxy = {
    enable = mkEnableOption ''
      one MCP endpoint per upstream server at http://127.0.0.1:PORT/NAME/mcp,
      for every agent and sandbox. The proxy signs in to remote servers
      itself and refreshes their tokens, so clients hold only the proxy's
      own token. Authorize each remote server once with
      `mcp-proxy-authorize NAME`
    '';

    port = mkOption {
      type = types.port;
      default = 6290;
    };

    stateDir = mkOption {
      type = types.str;
      default = "${config.xdg.stateHome}/mcp-proxy";
      readOnly = true;

      description = ''
        Holds `token`, the bearer token every client must send. Sandboxed
        commands must not be able to read it.
      '';
    };

    oauthDir = mkOption {
      type = types.str;
      readOnly = true;

      default =
        if darwin
        then "${config.home.homeDirectory}/Library/Application Support/mcp-proxy"
        else "${config.xdg.configHome}/mcp-proxy";

      description = ''
        Where the proxy keeps OAuth tokens and registered clients (Go's user
        config directory, which ignores XDG on macOS). Sandboxed commands
        must not be able to read it.
      '';
    };

    endpoints = mkOption {
      type = types.attrsOf types.str;
      readOnly = true;
      default = mapAttrs (name: _: "/${name}/mcp") servers;

      description = ''
        Each server's path on the proxy, for clients to register.
      '';
    };

    url = mkOption {
      type = types.str;
      readOnly = true;
      default = "http://127.0.0.1:${toString cfg.port}";
    };

    tokenFile = mkOption {
      type = types.str;
      readOnly = true;
      default = tokenFile;
    };

    headersHelper = mkOption {
      type = types.str;
      readOnly = true;
      default = "${headersHelper}";

      description = ''
        Command printing `{"Authorization": "Bearer TOKEN"}`, for clients
        that take their headers from a command.
      '';
    };

    linear = mkOption {
      type = types.bool;
      default = true;
    };

    buildkite = mkOption {
      type = types.bool;
      default = true;
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      home.packages = [authorize status];

      home.activation.mcpProxyToken = hm.dag.entryAfter ["writeBoundary"] ''
        if [ ! -s ${escapeShellArg tokenFile} ] && [ -z "''${DRY_RUN:-}" ]; then
          mkdir -p ${escapeShellArg cfg.stateDir}
          chmod 700 ${escapeShellArg cfg.stateDir}
          (umask 077 && ${pkgs.openssl}/bin/openssl rand -hex 32 > ${escapeShellArg tokenFile})
        fi
      '';
    }

    (mkIf darwin {
      launchd.agents.mcp-proxy = {
        enable = true;
        domain = "user";

        config = {
          ProgramArguments = [(getExe serve)];
          RunAtLoad = true;
          KeepAlive = true;
          StandardOutPath = "${config.xdg.stateHome}/mcp-proxy.log";
          StandardErrorPath = "${config.xdg.stateHome}/mcp-proxy.log";
        };
      };
    })

    (mkIf (!darwin) {
      systemd.user.services.mcp-proxy = {
        Unit = {
          Description = "MCP proxy holding the logins for remote MCP servers";
          After = ["network.target"];
        };

        Service = {
          ExecStart = getExe serve;
          Restart = "always";
        };

        Install.WantedBy = ["default.target"];
      };
    })
  ]);
}
