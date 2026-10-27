{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.mcp-proxy;
  darwin = pkgs.stdenv.hostPlatform.isDarwin;

  proxy = getExe pkgs.tbxark-mcp-proxy;
  tokenFile = "${cfg.stateDir}/token";

  # Remote servers are reached with short-lived OAuth tokens that the proxy
  # obtains and refreshes itself; nothing here is a credential. Each serves
  # only the tools the guard's mcp.json lists for its service, reads and
  # confirmed writes alike: the guard decides which ask, and keeps
  # reviewers to the reads. A tool missing from the list stays
  # hidden; an allow list must not be empty, which exposes every tool.
  oauthServer = id: url: scopes: {
    inherit url;
    transportType = "streamable-http";
    oauth.scopes = scopes;
    options.toolFilter = {
      mode = "allow";
      list = pkgs.agent-tools.mcpTools.${id}.reads ++ pkgs.agent-tools.mcpTools.${id}.confirms;
    };
  };

  servers =
    optionalAttrs cfg.linear {
      linear = oauthServer "linear" "https://mcp.linear.app/mcp" ["read" "write"];
    }
    // optionalAttrs cfg.buildkite {
      buildkite = oauthServer "buildkite" "https://mcp.buildkite.com/mcp/readonly" ["read"];
    }
    // optionalAttrs cfg.notion {
      # Notion's server has one scope, granted without being named.
      notion = oauthServer "notion" "https://mcp.notion.com/mcp" [];
    }
    // optionalAttrs cfg.hex {
      hex = oauthServer "hex" "https://app.hex.tech/mcp" ["openid" "offline_access"];
    }
    // optionalAttrs cfg.datadog.enable {
      # Without toolsets the server serves `core` alone.
      datadog = oauthServer "datadog" "https://mcp.${cfg.datadog.site}/v1/mcp?toolsets=core,error-tracking" ["mcp_all"];
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

  # Home Manager's domain values, gui and user, are also launchctl's domain
  # prefixes.
  agent = config.launchd.agents.mcp-proxy;
  restart =
    if darwin
    then "launchctl kickstart -k \"${agent.domain}/$(id -u)/${agent.config.Label}\""
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
      # A client that found no route when it started reports the server as
      # not found, and connects again only when told to.
      echo "Sessions started before now reach $1 after a reconnect: /mcp in Claude Code, or a new session." >&2
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

    notion = mkOption {
      type = types.bool;
      default = false;
    };

    hex = mkOption {
      type = types.bool;
      default = false;
    };

    datadog = {
      enable = mkOption {
        type = types.bool;
        default = false;
      };

      site = mkOption {
        type = types.str;
        default = "datadoghq.com";
        example = "us5.datadoghq.com";

        description = ''
          The organization's Datadog site, whose MCP server is
          https://mcp.SITE/v1/mcp.
        '';
      };
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
      # Left in the default gui domain. In the user domain, where home-manager
      # adds LimitLoadToSessionType = Background, launchd did not load it
      # after a reboot, and the proxy stayed down until it was loaded by hand.
      launchd.agents.mcp-proxy = {
        enable = true;

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
