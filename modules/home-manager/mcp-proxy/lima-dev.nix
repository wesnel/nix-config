# The two managed guests share transport and confinement. Their homes,
# outboxes and provisioning are separate; only development has private dial.
{debug ? false}: {
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  option =
    if debug
    then "limaDebug"
    else "limaDev";
  command =
    if debug
    then "lima-debug"
    else "lima-dev";
  cfg = config.wgn.home.mcp-proxy.${option};
  llm = config.wgn.home.llm;
  agentTools = llm.agentTools;

  # The guest reaches only these, both forwarded by QEMU to lima-mcp's
  # relays on the host; the shim listens where the QUIC names resolve.
  connect = "192.168.5.100:3128";
  quic = "192.168.5.101:443";
  shimAddress = "127.0.1.1";
  shimListen = "${shimAddress}:443";
  proxyURL = "http://${connect}";
  caCert = "${cfg.home}/_mcp/ca.pem";

  # What the guard needs, in a store file of its own, which the guard
  # refuses unless it is in the store: nothing that writes the Lima home,
  # QEMU included, can choose what the guard executes. It names no guard,
  # so the guard can name it; configFile, a superset, then names the guard.
  guardSettings =
    {
      home = config.home.homeDirectory;
      limaHome = cfg.home;
      inherit (cfg) instance;
      qemu = "${pkgs.qemu}/bin/qemu-system-aarch64";
      deniedReads = map (p:
        if hasPrefix "~/" p
        then "${config.home.homeDirectory}/${removePrefix "~/" p}"
        else p)
      llm.sandbox.deniedReads;
      inherit connect;
    }
    // optionalAttrs (!debug) {inherit quic;};
  guardConfig = pkgs.writeText "lima-guard.json" (builtins.toJSON guardSettings);

  # Lima looks for firmware in share/qemu beside the bin directory of
  # QEMU_SYSTEM_AARCH64. The wrapper is compiled, so no shell runs between
  # Lima and the guard.
  guard = pkgs.runCommand "lima-qemu-guard" {nativeBuildInputs = [pkgs.makeBinaryWrapper];} ''
    mkdir -p $out/bin $out/share
    ln -s ${pkgs.qemu}/share/qemu $out/share/qemu
    makeBinaryWrapper ${getExe' agentTools "lima-mcp"} $out/bin/qemu-system-aarch64 \
      --add-flags ${escapeShellArg "qemu-guard --config ${guardConfig} --"}
  '';

  # What a session runs in a repository to write NAME.bundle into the
  # outbox for lima_dev_import, which names it.
  export = pkgs.runCommand "${command}-export" {nativeBuildInputs = [pkgs.makeBinaryWrapper];} ''
    makeBinaryWrapper ${getExe' agentTools "lima-mcp"} $out/bin/${command}-export \
      --add-flags ${escapeShellArg "export --outbox ${cfg.outbox}"}
  '';

  # The operator's limactl for the managed home, never plain limactl, which
  # would start QEMU without the guard: lima-mcp lima-dev refreshes the
  # stable links and makes the CA as lima-mcp's start does, then runs the
  # pinned limactl with the guard as QEMU.
  wrapper = pkgs.runCommand command {nativeBuildInputs = [pkgs.makeBinaryWrapper];} ''
    makeBinaryWrapper ${getExe' agentTools "lima-mcp"} $out/bin/${command} \
      --add-flags ${escapeShellArg "lima-dev --config ${configFile} --"}
  '';

  # Provisioning runs as root on every boot, and writes whole files, so
  # each run leaves the same state.
  systemScript =
    ''
      #!/bin/sh
      set -eu
      cat > /etc/environment <<'EOF'
      PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
      http_proxy=${proxyURL}
      https_proxy=${proxyURL}
      HTTP_PROXY=${proxyURL}
      HTTPS_PROXY=${proxyURL}
      no_proxy=localhost,127.0.0.1,127.0.1.1
      NO_PROXY=localhost,127.0.0.1,127.0.1.1
      EOF
      cat > /etc/apt/apt.conf.d/90lima-proxy <<'EOF'
      Acquire::http::Proxy "${proxyURL}";
      Acquire::https::Proxy "${proxyURL}";
      EOF
      rm -f /etc/apt/sources.list
      cat > /etc/apt/sources.list.d/ubuntu.sources <<'EOF'
      Types: deb
      URIs: https://ports.ubuntu.com/ubuntu-ports/
      Suites: resolute resolute-updates resolute-backports resolute-security
      Components: main restricted universe multiverse
      Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
      EOF
    ''
    + optionalString (!debug) ''
      {
        echo "127.0.0.1 localhost $(hostname)"
        echo "::1 localhost ip6-localhost ip6-loopback"
      ${concatMapStrings (s: "  echo '${shimAddress} ${s}'\n") cfg.privateDial.servers}} > /etc/hosts
      mkdir -p /etc/ngrok-agent
      cat > /etc/ngrok-agent/lima.yml <<'EOF'
      ${agentConfig}
      EOF
      cat > /etc/systemd/system/lima-udp-shim.service <<'EOF'
      [Unit]
      Description=Carry private-dial QUIC to the host's broker
      After=network.target
      ConditionPathExists=/usr/local/bin/lima-udp-shim

      [Service]
      ExecStart=/usr/local/bin/lima-udp-shim -listen ${shimListen} -connect ${quic}
      DynamicUser=yes
      AmbientCapabilities=CAP_NET_BIND_SERVICE
      Restart=always

      [Install]
      WantedBy=multi-user.target
      EOF
      systemctl daemon-reload
      systemctl enable lima-udp-shim.service
    ''
    + optionalString debug ''
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends strace bpftrace gdb delve lldb linux-tools-common "linux-tools-$(uname -r)"
      install -d -m 0755 /etc/sudoers.d
      printf '%s\n' '%sudo ALL=(ALL:ALL) NOPASSWD: ALL' > /etc/sudoers.d/90-agent-debug
      chmod 0440 /etc/sudoers.d/90-agent-debug
      visudo -cf /etc/sudoers.d/90-agent-debug
      mkdir -p /sys/kernel/tracing
      if ! mountpoint -q /sys/kernel/tracing; then
        mount -t tracefs tracefs /sys/kernel/tracing
      fi
      test -r /sys/kernel/btf/vmlinux
      perf version
    '';

  userScript =
    ''
      #!/bin/sh
      set -eu
      export https_proxy=${proxyURL}
      mkdir -p "$HOME/${cfg.workdir}"
      if [ ! -x "$HOME/.local/bin/mise" ]; then
        curl -fsSL https://mise.run | sh
      fi
    ''
    + optionalString debug ''
      sudo -n true
    '';

  # ngrok-agent's settings that are not credentials; the operator writes
  # the authtoken and API key into ~/.config/ngrok/agent.yml in the guest.
  agentConfig = builtins.toJSON {
    version = 4;
    agent.connect = {
      proxy_url = proxyURL;
      private_endpoint_server_url = cfg.privateDial.serverURL;
      private_endpoint_server_transport = "h3";
    };
  };

  template = (pkgs.formats.yaml {}).generate "${cfg.instance}.yaml" ({
      vmType = "qemu";
      arch = "aarch64";
      # No guest agent, its virtio port or its port forwarding; Lima ignores
      # mounts. P0 showed caCerts and provisioning still apply.
      plain = true;
      cpus = 4;
      memory = "8GiB";
      disk = "60GiB";
      images = [
        {
          location = "https://cloud-images.ubuntu.com/releases/resolute/release-20260720/ubuntu-26.04-server-cloudimg-arm64.img";
          arch = "aarch64";
          digest = "sha256:7bcf159e29ad0000bfed9c57875908c39268f5ed1257f4958fa6a9f5f60edd54";
        }
      ];
      mounts = [];
      networks = [];
      ssh = {
        loadDotSSHPubKeys = false;
        forwardAgent = false;
        forwardX11 = false;
      };
      hostResolver.enabled = false;
      portForwards = [
        {
          guestIP = "0.0.0.0";
          guestPortRange = [1 65535];
          proto = "any";
          ignore = true;
        }
      ];
      propagateProxyEnv = false;
      containerd = {
        system = false;
        user = false;
      };
      upgradePackages = false;
      video.display = "none";
      provision = [
        {
          mode = "system";
          script = systemScript;
        }
        {
          mode = "user";
          script = userScript;
        }
      ];
    }
    // optionalAttrs (!debug) {caCerts.files = [caCert];});

  configFile = pkgs.writeText "${command}.json" (builtins.toJSON (guardSettings
    // {
      inherit (cfg) workdir;
      limactl = getExe' pkgs.lima "limactl";
      guard = "${guard}/bin/qemu-system-aarch64";
      limaMcp = getExe' agentTools "lima-mcp";
      template = "${template}";
      git = getExe pkgs.git;
      inherit (cfg) outbox;
      # limaegress.ParsePolicy adds the denials: ngrok's connect endpoints
      # and API, and the QUIC servers.
      egress = {
        allow = llm.sandbox.allowedDomains ++ cfg.egress.extraHosts;
        quicServers =
          if debug
          then []
          else cfg.privateDial.servers;
      };
      privateDialEndpoints =
        if debug
        then []
        else cfg.privateDial.endpoints;
    }
    // optionalAttrs (!debug) {shim = "${agentTools.guestShim}/bin/linux_arm64/lima-udp-shim";}));
in {
  options.wgn.home.mcp-proxy.${option} = {
    enable = mkEnableOption (
      if debug
      then ''
        the isolated Linux debugging guest served by the debug MCP endpoint.
        Its guest commands may use passwordless sudo; it has no host mounts,
        forwarded credentials or private-dial access. Run its limactl as
        `lima-debug`; imports use `lima-debug-export`
      ''
      else ''
        the managed Lima VM for ngrok-agent development, served by the lima
        MCP server's lima_dev tools. It has a Lima home of its own, which
        sessions cannot read; its guest reaches only the HTTPS hosts of the
        egress policy, through a CONNECT relay, and private dial, through an
        HTTP/3 broker. Run limactl against it only as `lima-dev`
      ''
    );

    home = mkOption {
      type = types.str;
      default = "${config.xdg.stateHome}/${
        if debug
        then "lima-debug"
        else "lima-mcp"
      }/lima";
      description = ''
        The managed VM's LIMA_HOME. It holds its transport keys, so it is
        one of `wgn.home.llm.guard.credentialPaths`.
        Its path is written into QEMU's arguments and a Seatbelt profile, so
        it holds only letters, digits and `._/+-`.
      '';
    };

    instance = mkOption {
      type = types.str;
      default =
        if debug
        then "agent-debug"
        else "ngrok-agent";
      readOnly = true;
    };

    outbox = mkOption {
      type = types.str;
      default = "${config.xdg.stateHome}/${
        if debug
        then "lima-debug"
        else "lima-mcp"
      }-outbox";
      description = ''
        Where sessions write the git bundles lima_dev_import carries into
        the VM, with `${command}-export NAME` run in a repository. It is one
        of {option}`wgn.home.llm.sandbox.writableRoots`, which Codex's and
        Claude Code's sandboxes let sessions write, and activation makes
        it, mode 0700. lima-mcp takes only a bundle that passes its checks
        from it. It holds only letters, digits and `._/+-`.
      '';
    };

    workdir = mkOption {
      type = types.str;
      default =
        if debug
        then "src/project"
        else "src/ngrok-agent";
      description = "Where guest commands run, relative to the guest user's home.";
    };

    egress.extraHosts = mkOption {
      type = types.listOf types.str;
      default =
        [
          "ports.ubuntu.com"
          "mise.run"
          "mise.jdx.dev"
          "mise-versions.jdx.dev"
          "go.dev"
          "dl.google.com"
        ]
        ++ optionals (!debug) ["connect.ngrok-agent.com" "*.connect.ngrok-agent.com"];
      description = ''
        HTTPS hosts the guest may reach besides
        `wgn.home.llm.sandbox.allowedDomains`, each exact or `*.suffix`.
        ngrok's connect endpoints and API, and the private-dial servers,
        are refused whatever this says.
      '';
    };

    privateDial = optionalAttrs (!debug) {
      servers = mkOption {
        type = types.listOf (types.strMatching "[a-z0-9-]+\\.quic\\.connect-endpoint\\.ngrok\\.com");
        default = ["us.quic.connect-endpoint.ngrok.com"];
        description = ''
          The private-dial QUIC servers the broker terminates; the broker
          CA may vouch for no other name.
        '';
      };

      serverURL = mkOption {
        type = types.strMatching "[a-z0-9.-]+:[0-9]+";
        default = "us.h2.connect-endpoint.ngrok.com:443";
        description = ''
          The private-dial server, `host:port`, written to the guest
          ngrok-agent's configuration as `private_endpoint_server_url`.
        '';
      };

      endpoints = mkOption {
        type = types.listOf types.str;
        default = [];
        example = ["db.internal:5432"];
        description = ''
          The private endpoints, `host:port`, a private dial may reach.
          Empty refuses every dial.
        '';
      };
    };

    configFile = mkOption {
      type = types.path;
      readOnly = true;
      default = configFile;
      description = "The configuration lima-mcp is started with.";
    };

    template = mkOption {
      type = types.path;
      readOnly = true;
      default = template;
      description = "The instance's Lima template.";
    };

    guardConfig = mkOption {
      type = types.path;
      readOnly = true;
      default = guardConfig;
      description = "The launch guard's configuration, which its wrapper names.";
    };
  };

  config = mkIf cfg.enable {
    assertions =
      [
        {
          assertion = pkgs.stdenv.hostPlatform.isDarwin && config.wgn.home.mcp-proxy.enable && (debug || config.wgn.home.mcp-proxy.lima);
          message = "wgn.home.mcp-proxy.${option} requires macOS and the profile's MCP endpoint";
        }
        {
          assertion = builtins.match "/[A-Za-z0-9._/+-]*" cfg.home != null;
          message = "wgn.home.mcp-proxy.${option}.home holds a character the guard cannot write into QEMU's arguments";
        }
        {
          # lima-mcp's configuration takes it only so, and lima-dev-export's
          # wrapper splits its flags at spaces. That it is in no credential
          # path, the Lima home included, wgn.home.llm asserts of every
          # writable root.
          assertion = builtins.match "/[A-Za-z0-9._/+-]*" cfg.outbox != null;
          message = "wgn.home.mcp-proxy.${option}.outbox holds a character other than letters, digits and ._/+-";
        }
      ]
      ++ optionals (debug && config.wgn.home.mcp-proxy.limaDev.enable) [
        {
          assertion = let
            dev = config.wgn.home.mcp-proxy.limaDev;
          in
            !(hasPrefix "${dev.home}/" "${cfg.home}/")
            && !(hasPrefix "${cfg.home}/" "${dev.home}/")
            && cfg.outbox != dev.outbox;
          message = "wgn.home.mcp-proxy.limaDebug requires its own Lima home and outbox, separate from limaDev";
        }
      ]
      ++ optionals (!debug) [
        {
          assertion = cfg.privateDial.servers != [];
          message = "wgn.home.mcp-proxy.limaDev.privateDial.servers is empty";
        }
      ];
    home.packages = [wrapper export];
    wgn.home.llm.sandbox.writableRoots = [cfg.outbox];
    home.activation.${
      if debug
      then "limaDebugOutbox"
      else "limaMcpOutbox"
    } = hm.dag.entryAfter ["writeBoundary"] ''
      run mkdir -p ${escapeShellArg cfg.outbox}
      run chmod 700 ${escapeShellArg cfg.outbox}
    '';
  };
}
