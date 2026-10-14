{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.ring-mcp;
  mcpProxy = config.wgn.home.mcp-proxy;
  secrets = config.sops.secrets;

  mcpConfig = pkgs.writeText "ring-mcp-servers.json" (builtins.toJSON {
    mcpServers = optionalAttrs mcpProxy.enable (mapAttrs (_: path: {
        type = "http";
        url = mcpProxy.url + path;
        inherit (mcpProxy) headersHelper;
      })
      mcpProxy.endpoints);
  });

  # Buildkite's read-only endpoint still hands out credentials, and an
  # answer is shown in the phone app's feed.
  deniedTools = [
    "mcp__buildkite__access_token"
    "mcp__buildkite__get_cluster_secret"
    "mcp__buildkite__list_cluster_secrets"
    "mcp__buildkite__get_job_env"
  ];

  systemPrompt = ''
    The question was spoken into a ring, and your answer is read on a phone
    or a watch. Answer in at most three short sentences of plain text,
    without markdown. Notes the user captured are in ${cfg.inbox} in the
    working directory. Never put a token, secret or credential in an answer.
  '';

  # --restricted drops every tool that runs commands or fetches URLs, ignores
  # the user's settings and hooks, and confines the file tools to the
  # working directory. dontAsk refuses whatever --allowedTools does not name.
  askArgs =
    [
      "-p"
      "--restricted"
      "--tools"
      "Read,Grep,Glob"
      "--strict-mcp-config"
      "--mcp-config"
      "${mcpConfig}"
      "--disallowedTools"
    ]
    ++ deniedTools
    ++ optionals mcpProxy.enable (["--allowedTools"] ++ map (name: "mcp__${name}") (attrNames mcpProxy.endpoints))
    ++ [
      "--permission-mode"
      "dontAsk"
      "--no-session-persistence"
      "--append-system-prompt"
      systemPrompt
    ]
    ++ optionals (cfg.ask.model != null) ["--model" cfg.ask.model];

  # Reads the question on standard input. coreutils is also what the MCP
  # proxy's headersHelper runs with.
  ask = pkgs.writeShellApplication {
    name = "ring-mcp-ask";
    runtimeInputs = [pkgs.coreutils];
    text = ''
      mkdir -p ${escapeShellArg cfg.repo}
      cd ${escapeShellArg cfg.repo}
      exec ${escapeShellArg cfg.ask.claude} ${escapeShellArgs askArgs}
    '';
  };

  # ngrok refuses a request without the bearer token before it reaches this
  # machine; the server checks it again. The agent's local web API and its
  # store of captured requests stay off: any local process could read the
  # token from them, replay requests and start tunnels.
  ngrokConfig = builtins.toJSON {
    version = 3;
    agent = {
      authtoken = config.sops.placeholder.ngrok-authtoken;
      web_addr = false;
      inspect_db_size = -1;
    };
    endpoints = [
      {
        name = "ring-mcp";
        url = "https://${config.sops.placeholder.ring-mcp-domain}";
        upstream.url = "http://127.0.0.1:${toString cfg.port}";
        traffic_policy.on_http_request = [
          {
            expressions = [
              "!('authorization' in req.headers) || req.headers['authorization'][0] != 'Bearer ${config.sops.placeholder.ring-mcp-token}'"
            ];
            actions = [
              {
                type = "deny";
                config.status_code = 401;
              }
            ];
          }
        ];
      }
    ];
  };

  # Secrets are decrypted only once the YubiKey is reachable, so both
  # services retry until they are there.
  afterSecrets = {
    After = ["network.target" "sops-nix.service"];
  };
  retry = {
    Restart = "always";
    RestartSec = 30;
  };
in {
  options.wgn.home.ring-mcp = {
    enable = mkEnableOption ''
      the MCP server for a Pebble Index ring, published through ngrok.
      Needs `ngrok-authtoken`, `ring-mcp-domain` (a domain reserved on that
      ngrok account) and `ring-mcp-token` in the sops file. The token goes
      in the Pebble app as "Bearer TOKEN"; it is compared inside a quoted
      traffic policy expression, so it must not contain a quote
    '';

    port = mkOption {
      type = types.port;
      default = 6291;
    };

    repo = mkOption {
      type = types.str;
      default = "${config.home.homeDirectory}/git/org";
      description = ''
        Git repository the captures are committed to, unsigned, and the only
        directory `ask` can read. Created if missing.
      '';
    };

    inbox = mkOption {
      type = types.str;
      default = "inbox.org";
      description = "Org file in `repo` that captures are appended to.";
    };

    ask = {
      claude = mkOption {
        type = types.str;
        default = "${config.home.homeDirectory}/.local/bin/claude";
        description = "Claude Code, which installs and updates itself outside Nix.";
      };

      model = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "haiku";
      };

      timeout = mkOption {
        type = types.str;
        default = "60s";
        description = "How long a question may take, as a Go duration.";
      };
    };
  };

  config = mkIf cfg.enable {
    sops = {
      secrets = {
        ngrok-authtoken = {};
        ring-mcp-domain = {};
        ring-mcp-token = {};
      };

      templates."ring-mcp-ngrok.yml".content = ngrokConfig;
    };

    home.packages = [ask];

    systemd.user.services = {
      ring-mcp = {
        Unit =
          afterSecrets
          // {
            Description = "MCP server for the Pebble Index ring";
          };

        Service =
          retry
          // {
            ExecStart = escapeShellArgs [
              (getExe pkgs.ring-mcp)
              "--listen"
              "127.0.0.1:${toString cfg.port}"
              "--token-file"
              secrets.ring-mcp-token.path
              "--repo"
              cfg.repo
              "--inbox"
              cfg.inbox
              "--ask-command"
              (getExe ask)
              "--ask-timeout"
              cfg.ask.timeout
            ];
            Environment = ["PATH=${makeBinPath [pkgs.git pkgs.coreutils]}"];
          };

        Install.WantedBy = ["default.target"];
      };

      ring-mcp-tunnel = {
        Unit =
          afterSecrets
          // {
            Description = "ngrok endpoint for the Pebble Index ring's MCP server";
          };

        Service =
          retry
          // {
            ExecStart = escapeShellArgs [
              (getExe pkgs.ngrok)
              "start"
              "--all"
              "--config"
              config.sops.templates."ring-mcp-ngrok.yml".path
              "--log"
              "stdout"
            ];
          };

        Install.WantedBy = ["default.target"];
      };
    };
  };
}
