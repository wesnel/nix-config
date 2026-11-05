# The managed Lima VM for ngrok-agent development, which lima-mcp serves
# through the lima MCP server: its configuration, Lima template, launch
# guard, guest shim and the operator's lima-dev wrapper. lima-mcp reads
# the configuration (configFile) once at start; the guard reads its own
# (guardConfig) from the store; the egress relays read configFile through
# the stable link <home>/_mcp/config.json, which lima-mcp and lima-dev
# refresh before each start and command, so a policy change reaches a
# running VM after a switch.
{
  config,
  lib,
  pkgs,
  ...
}:
with lib; let
  cfg = config.wgn.home.mcp-proxy.limaDev;
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
  guardSettings = {
    home = config.home.homeDirectory;
    limaHome = cfg.home;
    inherit (cfg) instance;
    qemu = "${pkgs.qemu}/bin/qemu-system-aarch64";
    deniedReads = map (p:
      if hasPrefix "~/" p
      then "${config.home.homeDirectory}/${removePrefix "~/" p}"
      else p)
    llm.sandbox.deniedReads;
    inherit connect quic;
  };
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

  # The operator's limactl for the managed home, never plain limactl, which
  # would start QEMU without the guard: lima-mcp lima-dev refreshes the
  # stable links and makes the CA as lima-mcp's start does, then runs the
  # pinned limactl with the guard as QEMU.
  wrapper = pkgs.runCommand "lima-dev" {nativeBuildInputs = [pkgs.makeBinaryWrapper];} ''
    makeBinaryWrapper ${getExe' agentTools "lima-mcp"} $out/bin/lima-dev \
      --add-flags ${escapeShellArg "lima-dev --config ${configFile} --"}
  '';

  # Provisioning runs as root on every boot, and writes whole files, so
  # each run leaves the same state.
  systemScript = ''
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
  '';

  userScript = ''
    #!/bin/sh
    set -eu
    export https_proxy=${proxyURL}
    mkdir -p "$HOME/${cfg.workdir}"
    if [ ! -x "$HOME/.local/bin/mise" ]; then
      curl -fsSL https://mise.run | sh
    fi
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

  template = (pkgs.formats.yaml {}).generate "${cfg.instance}.yaml" {
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
    caCerts.files = [caCert];
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
  };

  configFile = pkgs.writeText "lima-dev.json" (builtins.toJSON (guardSettings
    // {
      inherit (cfg) workdir;
      limactl = getExe' pkgs.lima "limactl";
      guard = "${guard}/bin/qemu-system-aarch64";
      limaMcp = getExe' agentTools "lima-mcp";
      template = "${template}";
      shim = "${agentTools.guestShim}/bin/linux_arm64/lima-udp-shim";
      # limaegress.ParsePolicy adds the denials: ngrok's connect endpoints
      # and API, and the QUIC servers.
      egress = {
        allow = llm.sandbox.allowedDomains ++ cfg.egress.extraHosts;
        quicServers = cfg.privateDial.servers;
      };
      privateDialEndpoints = cfg.privateDial.endpoints;
    }));
in {
  options.wgn.home.mcp-proxy.limaDev = {
    enable = mkEnableOption ''
      the managed Lima VM for ngrok-agent development, served by the lima
      MCP server's lima_dev tools. It has a Lima home of its own, which
      sessions cannot read; its guest reaches only the HTTPS hosts of the
      egress policy, through a CONNECT relay, and private dial, through an
      HTTP/3 broker. Run limactl against it only as `lima-dev`
    '';

    home = mkOption {
      type = types.str;
      default = "${config.xdg.stateHome}/lima-mcp/lima";
      description = ''
        The managed VM's LIMA_HOME. It holds the instance's SSH key and the
        broker CA's key, so it is one of `wgn.home.llm.guard.credentialPaths`.
        Its path is written into QEMU's arguments and a Seatbelt profile, so
        it holds only letters, digits and `._/+-`.
      '';
    };

    instance = mkOption {
      type = types.str;
      default = "ngrok-agent";
      readOnly = true;
    };

    workdir = mkOption {
      type = types.str;
      default = "src/ngrok-agent";
      description = "Where guest commands run, relative to the guest user's home.";
    };

    egress.extraHosts = mkOption {
      type = types.listOf types.str;
      default = [
        "ports.ubuntu.com"
        "mise.run"
        "mise.jdx.dev"
        "mise-versions.jdx.dev"
        "go.dev"
        "dl.google.com"
        "connect.ngrok-agent.com"
        "*.connect.ngrok-agent.com"
      ];
      description = ''
        HTTPS hosts the guest may reach besides
        `wgn.home.llm.sandbox.allowedDomains`, each exact or `*.suffix`.
        ngrok's connect endpoints and API, and the private-dial servers,
        are refused whatever this says.
      '';
    };

    privateDial = {
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
    assertions = [
      {
        assertion = config.wgn.home.mcp-proxy.lima && pkgs.stdenv.hostPlatform.isDarwin;
        message = "wgn.home.mcp-proxy.limaDev requires wgn.home.mcp-proxy.lima, on macOS";
      }
      {
        assertion = builtins.match "/[A-Za-z0-9._/+-]*" cfg.home != null;
        message = "wgn.home.mcp-proxy.limaDev.home holds a character the guard cannot write into QEMU's arguments";
      }
      {
        assertion = cfg.privateDial.servers != [];
        message = "wgn.home.mcp-proxy.limaDev.privateDial.servers is empty";
      }
    ];
    home.packages = [wrapper];
  };
}
