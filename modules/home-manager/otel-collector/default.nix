{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.otel-collector;
  darwin = pkgs.stdenv.hostPlatform.isDarwin;

  collector = getExe pkgs.opentelemetry-collector-contrib;

  # Each signal's file is rotated at maxMegabytes and keeps maxBackups
  # rotated files, deleted after maxDays: about 1 GiB on disk for the two
  # signals. A file still being written is bounded by its size alone.
  maxMegabytes = 100;
  maxBackups = 4;
  maxDays = 60;

  fileExporter = signal: {
    path = "${cfg.dir}/${signal}.json";
    format = "json";
    create_directory = true;
    directory_permissions = "0700";
    rotation = {
      max_megabytes = maxMegabytes;
      max_backups = maxBackups;
      max_days = maxDays;
    };
  };

  signals = ["metrics" "logs"];

  configFile = (pkgs.formats.yaml {}).generate "otel-collector.yaml" {
    receivers.otlp.protocols.http.endpoint = "127.0.0.1:${toString cfg.port}";
    exporters = listToAttrs (map (s: nameValuePair "file/${s}" (fileExporter s)) signals);
    service = {
      pipelines = genAttrs signals (s: {
        receivers = ["otlp"];
        exporters = ["file/${s}"];
      });
      # The collector's own metrics would otherwise be served on a port of
      # their own.
      telemetry.metrics.level = "none";
    };
  };
in {
  options.wgn.home.otel-collector = {
    enable = mkEnableOption ''
      an OpenTelemetry collector on loopback that keeps the metrics and logs
      Claude Code and Codex export in local files, one per signal under
      {option}`wgn.home.otel-collector.dir`, as OTLP JSON lines, each
      rotated at ${toString maxMegabytes} MB with ${toString maxBackups}
      rotated files kept for ${toString maxDays} days. Nothing leaves the
      host.

      Claude Code's and Codex's settings are merged into files they own, so
      turning this off leaves their keys there: remove from
      ~/.claude/settings.json the `env` keys `otelSettings` sets in
      modules/home-manager/claude/default.nix, and from ~/.codex/config.toml
      the `[otel]` keys the `codexOtel` activation writes in
      modules/home-manager/codex/default.nix
    '';

    port = mkOption {
      type = types.port;
      default = 6291;

      description = ''
        The loopback port receiving OTLP/HTTP. Not 4317 or 4318, the
        defaults a collector started for a test listens on.
      '';
    };

    url = mkOption {
      type = types.str;
      readOnly = true;
      default = "http://127.0.0.1:${toString cfg.port}";

      description = ''
        The OTLP/HTTP base URL, to which clients add `/v1/metrics` or
        `/v1/logs`.
      '';
    };

    dir = mkOption {
      type = types.str;
      readOnly = true;
      default = "${config.xdg.stateHome}/otel-collector";

      description = ''
        Holds `metrics.json` and `logs.json`, and their rotated files.
      '';
    };
  };

  config = mkIf cfg.enable (mkMerge [
    (mkIf darwin {
      # Left in the default gui domain, as mcp-proxy is, which launchd loads
      # at login.
      launchd.agents.otel-collector = {
        enable = true;

        config = {
          ProgramArguments = [collector "--config" "${configFile}"];
          RunAtLoad = true;
          KeepAlive = true;
          ProcessType = "Background";
          StandardOutPath = "${config.xdg.stateHome}/otel-collector.log";
          StandardErrorPath = "${config.xdg.stateHome}/otel-collector.log";
        };
      };
    })

    (mkIf (!darwin) {
      systemd.user.services.otel-collector = {
        Unit.Description = "OpenTelemetry collector keeping agents' telemetry in local files";

        Service = {
          ExecStart = "${collector} --config ${configFile}";
          Restart = "always";
        };

        Install.WantedBy = ["default.target"];
      };
    })
  ]);
}
